.PHONY: all build run test lint verify clean add-memo clean-base

BIN_NAME := minimal-web-kit

all: test build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/$(BIN_NAME) ./cmd/server

run:
	go run ./cmd/server

test:
	go test -race -v ./...

verify:
	go mod verify
	go vet ./...

clean:
	rm -rf bin/ data/

# テンプレート管理コマンド
add-memo:
	go run ./cmd/kit add-memo

clean-base:
	go run ./cmd/kit clean-base
