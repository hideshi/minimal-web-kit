.PHONY: all build run test lint verify clean add-site remove-site add-auth remove-auth add-cms remove-cms auth-setup auth-recover

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

# 管理者認証（パスキー）の登録切り替え（CMSが有効な間は外せない。登録を外してもDBのデータは残る）
add-auth:
	go run ./cmd/kit add-auth

remove-auth:
	go run ./cmd/kit remove-auth

# CMSの登録切り替え（管理者認証も一緒に有効にする。登録を外してもDBのデータは残る）
add-cms:
	go run ./cmd/kit add-cms

remove-cms:
	go run ./cmd/kit remove-cms

# 管理者のパスキーの初回登録用URLを表示（パスキーが未登録のときだけ）
auth-setup:
	go run ./cmd/server auth-setup

# 管理者のパスキーとセッションを全削除し、登録用URLを再発行（確認あり）
auth-recover:
	go run ./cmd/server auth-recover
