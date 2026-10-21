SHELL := /bin/bash
VERSION ?= 0.1.0-dev
LDFLAGS := -s -w -X github.com/liuhuo23/queqiao/internal/version.Version=$(VERSION)
GOFLAGS := -trimpath

.PHONY: all build server agent linux-server linux-agent darwin-agent windows-agent test vet tidy clean

all: build

build: server agent

server:
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/queqiaod ./cmd/queqiaod

agent:
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/queqiao ./cmd/queqiao

# 服务端目标平台：YOUR_SERVER_IP (linux/amd64)
linux-server:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/queqiaod-linux-amd64 ./cmd/queqiaod

linux-agent:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/queqiao-linux-amd64 ./cmd/queqiao

darwin-agent:
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/queqiao-darwin-arm64 ./cmd/queqiao

windows-agent:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/queqiao-windows-amd64.exe ./cmd/queqiao

test:
	go test ./...

vet:
	go vet ./...

tidy:
	go mod tidy

clean:
	rm -rf dist
