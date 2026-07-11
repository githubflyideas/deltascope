BINARY  := deltascope
VERSION := 1.0.0

.PHONY: build vendor test clean

build:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
	go build -trimpath -mod=vendor \
		-ldflags "-s -w -X main.version=$(VERSION)" \
		-o $(BINARY) .
	@echo "==> $(BINARY) ($$(du -h $(BINARY) | cut -f1)), 静态二进制, 可直接 scp 到离线主机"

vendor:
	go mod tidy
	go mod vendor

test:
	go vet ./...
	go test ./...

clean:
	rm -f $(BINARY)
