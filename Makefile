.PHONY: all build run test lint verify clean add-site remove-site add-cms remove-cms cms-setup cms-recover

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

# CMSの登録切り替え（登録を外してもDBのデータは残る）
add-cms:
	go run ./cmd/kit add-cms

remove-cms:
	go run ./cmd/kit remove-cms

# CMSの初回登録用URLを表示（パスキーが未登録のときだけ）
cms-setup:
	go run ./cmd/server cms-setup

# CMSのパスキーと管理者セッションを全削除し、登録用URLを再発行（確認あり）
cms-recover:
	go run ./cmd/server cms-recover
