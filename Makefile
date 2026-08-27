.PHONY: fmt fmt-check vet test build frontend-install frontend-check frontend-build check

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

frontend-install:
	npm --prefix frontend ci

frontend-check:
	npm --prefix frontend run typecheck
	npm --prefix frontend run lint
	npm --prefix frontend test -- --run

frontend-build:
	npm --prefix frontend run build

check: fmt-check vet test frontend-check frontend-build build
