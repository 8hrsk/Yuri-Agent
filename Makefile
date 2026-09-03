.PHONY: fmt fmt-check vet test build frontend-install frontend-check frontend-build check \
	wails-install wails-version wails-doctor macos-build macos-validate macos-package \
	macos-checksum macos-verify macos-launch-smoke macos-ui-smoke macos-voice-smoke macos-smoke mvp-smoke bench-baseline

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
YURI_VERSION ?= $(shell tr -d '[:space:]' < $(ROOT_DIR)/VERSION)
YURI_COMMIT ?= $(shell git -C $(ROOT_DIR) rev-parse HEAD)
YURI_BUILD_DATE ?= $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
MACOS_ARTIFACT ?= $(MACOS_DIST_DIR)/yuri-$(YURI_VERSION)-macos-universal.zip
MACOS_CHECKSUM ?= $(MACOS_ARTIFACT).sha256
MACOS_VALIDATOR ?= $(ROOT_DIR)/scripts/validate-macos-oss.sh
CHECKSUM_TOOL ?= $(ROOT_DIR)/scripts/checksum-artifact.sh
MACOS_LAUNCH_SMOKE ?= $(ROOT_DIR)/scripts/macos-launch-smoke.sh
MACOS_LAUNCH_TIMEOUT ?= 20

fmt:
	gofmt -w $$(find cmd internal sdk plugins -name '*.go' -type f)

fmt-check:
	@test -z "$$(gofmt -l cmd internal sdk plugins)" || (gofmt -l cmd internal sdk plugins && exit 1)

vet:
	go vet ./cmd/... ./internal/... ./sdk/... ./plugins/...

test:
	go test ./cmd/... ./internal/... ./sdk/... ./plugins/...

mvp-smoke:
	go test -race ./internal/smoke ./internal/desktop -run '^(TestMVPOfflineLifecycle|TestPluginPackageLifecycleSmoke|TestOpenAIProviderBridgeLifecycleSmoke|TestCodexBridgeAccountLifecycleSmoke|TestAutonomousPeerDialogueBridgeLifecycleSmoke)$$' -count=1

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

# This is the reproducible production-mode macOS OSS build. The project does
# not manage signing identities, notarization or distribution credentials.
macos-build: wails-version
	@test "$$(uname -s)" = Darwin || { echo "macos-build requires macOS" >&2; exit 1; }
	@config="$(ROOT_DIR)/cmd/yuri/wails.json"; \
	backup="$$(mktemp "$${TMPDIR:-/tmp}/yuri-wails.XXXXXX")"; \
	cp "$$config" "$$backup"; \
	trap 'cp "$$backup" "$$config"; rm -f "$$backup"' EXIT HUP INT TERM; \
	node "$(ROOT_DIR)/scripts/release/set-wails-version.mjs" "$$config" "$(YURI_VERSION)"; \
	cd "$(ROOT_DIR)/cmd/yuri" && \
	CGO_ENABLED=1 MACOSX_DEPLOYMENT_TARGET="$(MACOS_MIN_VERSION)" \
	CGO_CFLAGS="-mmacosx-version-min=$(MACOS_MIN_VERSION)" \
	CGO_LDFLAGS="-mmacosx-version-min=$(MACOS_MIN_VERSION)" \
	"$(WAILS_BIN)" build -clean -platform darwin/universal -trimpath \
		-ldflags "-s -w -X github.com/OrdoAI/yuri-agent/internal/buildinfo.Version=$(YURI_VERSION) -X github.com/OrdoAI/yuri-agent/internal/buildinfo.Commit=$(YURI_COMMIT) -X github.com/OrdoAI/yuri-agent/internal/buildinfo.Date=$(YURI_BUILD_DATE)" \
		-m -nosyncgomod
macos-validate: macos-build
	@"$(MACOS_VALIDATOR)" --app "$(MACOS_APP)" --version "$(YURI_VERSION)"

macos-package: macos-validate
	@mkdir -p "$(MACOS_DIST_DIR)"
	@rm -f "$(MACOS_ARTIFACT)"
	@ditto -c -k --sequesterRsrc --keepParent "$(MACOS_APP)" "$(MACOS_ARTIFACT)"

macos-checksum: macos-package
	@"$(CHECKSUM_TOOL)" "$(MACOS_ARTIFACT)" "$(MACOS_CHECKSUM)"

macos-verify: macos-validate macos-checksum
	@"$(CHECKSUM_TOOL)" --verify "$(MACOS_ARTIFACT)" "$(MACOS_CHECKSUM)"

macos-launch-smoke: macos-validate
	@"$(MACOS_LAUNCH_SMOKE)" --app "$(MACOS_APP)" --timeout "$(MACOS_LAUNCH_TIMEOUT)"

macos-ui-smoke: macos-validate
	@"$(MACOS_LAUNCH_SMOKE)" --app "$(MACOS_APP)" --timeout "$(MACOS_LAUNCH_TIMEOUT)" --ui-flow onboarding

macos-voice-smoke: macos-validate
	@"$(MACOS_LAUNCH_SMOKE)" --app "$(MACOS_APP)" --timeout "$(MACOS_LAUNCH_TIMEOUT)" --ui-flow voice

macos-smoke: macos-verify
	@echo "macOS universal OSS artifact: $(MACOS_ARTIFACT)"
	@echo "SHA-256 manifest: $(MACOS_CHECKSUM)"

check: fmt-check vet test frontend-check frontend-build build
