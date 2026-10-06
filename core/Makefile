BIN := bin/sbx
# 集成测试的上游代理（宿主机代理）；不需要时置空：make test-docker UPSTREAM_HOST=
UPSTREAM_HOST ?= host.docker.internal
UPSTREAM_PORT ?= 7890

.PHONY: build test test-docker lint e2e clean

build:
	go build -o $(BIN) ./cmd/sbx

test:
	go test ./...

test-docker:
	SBX_IT_UPSTREAM=$(if $(UPSTREAM_HOST),http://$(UPSTREAM_HOST):$(UPSTREAM_PORT)) \
	SBX_IT_UPSTREAM_HOST=$(UPSTREAM_HOST) SBX_IT_UPSTREAM_PORT=$(UPSTREAM_PORT) \
	go test -tags docker -timeout 40m ./...

lint:
	@if command -v golangci-lint >/dev/null; then golangci-lint run ./...; \
	else echo "golangci-lint 未安装，退回 go vet + gofmt"; go vet ./... && go vet -tags docker ./... && test -z "$$(gofmt -l .)"; fi

e2e: build
	./scripts/e2e-m1.sh

clean:
	rm -rf bin
