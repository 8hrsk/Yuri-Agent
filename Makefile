.PHONY: fmt fmt-check vet test build frontend-install frontend-check frontend-build check \
	wails-install wails-version wails-doctor macos-build macos-validate macos-package \
	macos-sign-dev macos-checksum macos-smoke macos-release-check release-check bench-baseline

ROOT_DIR := $(abspath $(dir $(lastword $(MAKEFILE_LIST))))

# Keep the Wails CLI separate from the Go module dependency. The CLI is a
# build tool, so it is installed into an ignored, repository-local directory
# and is never resolved from PATH or @latest.
WAILS_VERSION := v2.15.0
WAILS_MODULE := github.com/wailsapp/wails/v2/cmd/wails
TOOLS_DIR ?= $(ROOT_DIR)/.tools
WAILS_BIN ?= $(TOOLS_DIR)/wails

MACOS_MIN_VERSION := 11.0.0
MACOS_APP ?= $(ROOT_DIR)/cmd/yuri/build/bin/yuri.app
MACOS_DIST_DIR ?= $(ROOT_DIR)/dist/macos
YURI_VERSION ?= 0.7.0
MACOS_ARTIFACT ?= $(MACOS_DIST_DIR)/yuri-$(YURI_VERSION)-macos-universal.zip
MACOS_CHECKSUM ?= $(MACOS_ARTIFACT).sha256
MACOS_VALIDATOR ?= $(ROOT_DIR)/scripts/validate-macos-release.sh
CHECKSUM_TOOL ?= $(ROOT_DIR)/scripts/checksum-artifact.sh

fmt:
	gofmt -w $$(find cmd internal sdk plugins -name '*.go' -type f)

fmt-check:
	@test -z "$$(gofmt -l cmd internal sdk plugins)" || (gofmt -l cmd internal sdk plugins && exit 1)

vet:
	go vet ./cmd/... ./internal/... ./sdk/... ./plugins/...

test:
	go test ./cmd/... ./internal/... ./sdk/... ./plugins/...

build:
	go build -o bin/yuri ./cmd/yuri

bench-baseline:
	go test ./internal/memory ./internal/context -run '^$$' \
		-bench 'Benchmark(HybridRanker1000|LexicalScoreUnicode|AssemblerBoundedContext)$$' \
		-benchmem -count=3

wails-install:
	@mkdir -p "$(TOOLS_DIR)"
	@if [ ! -x "$(WAILS_BIN)" ] || ! "$(WAILS_BIN)" version 2>&1 | grep -F "$(WAILS_VERSION)" >/dev/null; then \
		GOBIN="$(TOOLS_DIR)" go install "$(WAILS_MODULE)@$(WAILS_VERSION)"; \
	fi

wails-version: wails-install
	@actual="$$("$(WAILS_BIN)" version 2>&1)"; \
	printf '%s\n' "$$actual"; \
	printf '%s\n' "$$actual" | grep -F "$(WAILS_VERSION)" >/dev/null || { \
		echo "Wails CLI version mismatch: expected $(WAILS_VERSION)" >&2; exit 1; \
	}

wails-doctor: wails-install
	@cd "$(ROOT_DIR)/cmd/yuri" && "$(WAILS_BIN)" doctor

frontend-install:
	npm --prefix frontend ci

frontend-check:
	npm --prefix frontend run typecheck
	npm --prefix frontend run lint
	npm --prefix frontend test -- --run

frontend-build:
	npm --prefix frontend run build

# This is intentionally a production-mode, unsigned development smoke build.
# Signing/notarization is a separate, externally controlled release step.
macos-build: wails-version
	@test "$$(uname -s)" = Darwin || { echo "macos-build requires macOS" >&2; exit 1; }
	@cd "$(ROOT_DIR)/cmd/yuri" && \
	CGO_ENABLED=1 MACOSX_DEPLOYMENT_TARGET="$(MACOS_MIN_VERSION)" \
	CGO_CFLAGS="-mmacosx-version-min=$(MACOS_MIN_VERSION)" \
	CGO_LDFLAGS="-mmacosx-version-min=$(MACOS_MIN_VERSION)" \
	"$(WAILS_BIN)" build -clean -platform darwin/universal -trimpath \
		-ldflags "-s -w" -m -nosyncgomod

macos-sign-dev: macos-build
	@codesign --force --deep --sign - --timestamp=none "$(MACOS_APP)"

macos-validate: macos-sign-dev
	@"$(MACOS_VALIDATOR)" --mode development --app "$(MACOS_APP)" --version "$(YURI_VERSION)"

macos-package: macos-sign-dev
	@mkdir -p "$(MACOS_DIST_DIR)"
	@rm -f "$(MACOS_ARTIFACT)"
	@ditto -c -k --sequesterRsrc --keepParent "$(MACOS_APP)" "$(MACOS_ARTIFACT)"

macos-checksum: macos-package
	@"$(CHECKSUM_TOOL)" "$(MACOS_ARTIFACT)" "$(MACOS_CHECKSUM)"

macos-smoke: macos-sign-dev
	@"$(MACOS_VALIDATOR)" --mode development --app "$(MACOS_APP)" --version "$(YURI_VERSION)"
	@mkdir -p "$(MACOS_DIST_DIR)"
	@rm -f "$(MACOS_ARTIFACT)"
	@ditto -c -k --sequesterRsrc --keepParent "$(MACOS_APP)" "$(MACOS_ARTIFACT)"
	@"$(CHECKSUM_TOOL)" "$(MACOS_ARTIFACT)" "$(MACOS_CHECKSUM)"
	@echo "macOS universal development smoke artifact: $(MACOS_ARTIFACT)"
	@echo "SHA-256 manifest: $(MACOS_CHECKSUM)"

# Validate an already signed/notarized artifact. This target deliberately does
# not build, sign, notarize, upload, or publish anything.
macos-release-check release-check:
	@"$(MACOS_VALIDATOR)" --mode release --app "$(MACOS_APP)" --version "$(YURI_VERSION)"
	@"$(CHECKSUM_TOOL)" --verify "$(MACOS_ARTIFACT)" "$(MACOS_CHECKSUM)"

check: fmt-check vet test frontend-check frontend-build build
