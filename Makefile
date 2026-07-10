# deltascope 构建
# 在联网开发机上首次构建前: make vendor
BINARY  := deltascope
VERSION := 1.0.0

.PHONY: build vendor test clean

build:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
	go build -trimpath -mod=vendor \
		-ldflags "-s -w -X main.version=$(VERSION)" \
		-o $(BINARY) .
	@echo "==> $(BINARY) ($$(du -h $(BINARY) | cut -f1)), 静态二进制, 可直接 scp 到离线主机"

# 拉取并固化依赖(仅需联网执行一次, vendor/ 随源码入库后离线可构建)
vendor:
	go mod tidy
	go mod vendor

test:
	go vet ./...
	go test ./...

clean:
	rm -f $(BINARY)
