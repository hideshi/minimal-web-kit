.PHONY: all build run test lint verify clean add-site remove-site

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

# 機能モジュールの着脱コマンド（自作機能には一切影響しません）
add-site:
	go run ./cmd/kit add-site

remove-site:
	go run ./cmd/kit remove-site
