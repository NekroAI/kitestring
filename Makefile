# 显示的版本号与 VERSION 文件、Release 包一致，不带前导 v（标签仍叫 v0.2.0）
VERSION ?= $(patsubst v%,%,$(shell git describe --tags --always --dirty 2>/dev/null || echo dev))
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w -X github.com/NekroAI/kitestring/internal/version.Version=$(VERSION) -X github.com/NekroAI/kitestring/internal/version.Commit=$(COMMIT)

.PHONY: build test vet clean deploy

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/ks ./cmd/ks

test:
	go test ./...

vet:
	go vet ./...

# 本机部署：构建、备份、替换、重启、核对，失败自动回滚。可传 DEPLOY_REF=<提交> 部署指定提交。
deploy: DEPLOY_REF ?=
deploy:
	scripts/deploy.sh $(DEPLOY_REF)

clean:
	rm -rf bin dist
