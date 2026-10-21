SHELL := /bin/bash
VERSION ?= 0.1.0-dev
LDFLAGS := -s -w -X nrp/internal/version.Version=$(VERSION)
GOFLAGS := -trimpath

.PHONY: all build server agent linux-server linux-agent darwin-agent windows-agent test vet tidy clean

all: build

build: server agent

server:
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/nrpd ./cmd/nrpd

agent:
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/nrp-agent ./cmd/nrp-agent

# 服务端目标平台：39.107.230.175 (linux/amd64)
linux-server:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/nrpd-linux-amd64 ./cmd/nrpd

linux-agent:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/nrp-agent-linux-amd64 ./cmd/nrp-agent

darwin-agent:
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/nrp-agent-darwin-arm64 ./cmd/nrp-agent

windows-agent:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/nrp-agent-windows-amd64.exe ./cmd/nrp-agent

test:
	go test ./...

vet:
	go vet ./...

tidy:
	go mod tidy

clean:
	rm -rf dist
