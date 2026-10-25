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

# Siteの登録切り替え（識別できない同名機能や想定外の登録状態は拒否）
add-site:
	go run ./cmd/kit add-site

remove-site:
	go run ./cmd/kit remove-site
