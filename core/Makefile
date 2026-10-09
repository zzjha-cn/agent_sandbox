BIN := bin/sbx
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
# 发布矩阵：macOS 和 Linux 的两种架构（design §12：单二进制，只依赖 docker CLI）
PLATFORMS := darwin/amd64 darwin/arm64 linux/amd64 linux/arm64
# 集成测试的上游代理（宿主机代理）；不需要时置空：make test-docker UPSTREAM_HOST=
UPSTREAM_HOST ?= host.docker.internal
UPSTREAM_PORT ?= 7890

.PHONY: build release test test-docker lint e2e e2e-m3 clean

build:
	go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/sbx

# release 交叉编译出 dist/sbx_<version>_<os>_<arch>[.tar.gz]，并附一份 sha256。
release:
	rm -rf dist && mkdir -p dist
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		name=sbx_$(VERSION)_$${os}_$${arch}; \
		echo "  $$name"; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o dist/$$name/sbx ./cmd/sbx || exit 1; \
		tar -C dist/$$name -czf dist/$$name.tar.gz sbx; \
		rm -rf dist/$$name; \
	done
	cd dist && shasum -a 256 *.tar.gz > SHA256SUMS
	@echo "产物在 dist/（版本 $(VERSION)）"

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

e2e-m3: build
	./scripts/e2e-m3.sh

clean:
	rm -rf bin dist
