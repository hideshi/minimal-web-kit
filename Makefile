.PHONY: all build run test test-ui lint verify vuln clean add-site remove-site add-auth remove-auth add-news remove-news auth-setup auth-recover add-catalog remove-catalog add-contact remove-contact add-pages remove-pages add-oidcop remove-oidcop add-federatedauth remove-federatedauth

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

# 既知の脆弱性を調べる（道具は版を固定して実行し、go.mod には足さない）。手順は skills/minimal-web-kit-vuln
GOVULNCHECK := golang.org/x/vuln/cmd/govulncheck@v1.9.0

vuln:
	go run $(GOVULNCHECK) ./...
	cd test/browser && go run $(GOVULNCHECK) ./...

# 画面部品をChrome・Chromiumで操作するテスト（別モジュール。Chromeが無ければ飛ばす。CHROME_PATHで指定可）
test-ui:
	cd test/browser && go vet ./... && go test -count=1 ./...

clean:
	rm -rf bin/ data/

# Siteの登録切り替え（識別できない同名機能や想定外の登録状態は拒否）
add-site:
	go run ./cmd/kit add-site

remove-site:
	go run ./cmd/kit remove-site

# 管理者認証（パスキー）の登録切り替え（お知らせ・カタログが有効な間は外せない。登録を外してもDBのデータは残る）
add-auth:
	go run ./cmd/kit add-auth

remove-auth:
	go run ./cmd/kit remove-auth

# 共通認証の提供機能。管理者認証を置き換え、アカウントで別システムにログインできるようにする（外すと通常の管理者認証に戻る）
add-oidcop:
	go run ./cmd/kit add-oidcop

remove-oidcop:
	go run ./cmd/kit remove-oidcop

# 共通認証の利用機能。管理者認証を置き換え、別システムの認証基盤でログインする
add-federatedauth:
	go run ./cmd/kit add-federatedauth

remove-federatedauth:
	go run ./cmd/kit remove-federatedauth

# お知らせ機能の登録切り替え（管理者認証も一緒に有効にする。登録を外してもDBのデータは残る）
add-news:
	go run ./cmd/kit add-news

remove-news:
	go run ./cmd/kit remove-news

# 最初の管理者のパスキーの登録用URLを表示（有効なパスキーが1件も無いときだけ）
auth-setup:
	go run ./cmd/server auth-setup

# パスキーとセッションを削除し、管理者の登録用URLを再発行（確認あり）。例: make auth-recover ARGS="-account 1"
auth-recover:
	go run ./cmd/server auth-recover $(ARGS)

# カタログ（管理者認証も有効化。無効化してもデータは保持）
add-catalog:
	go run ./cmd/kit add-catalog

remove-catalog:
	go run ./cmd/kit remove-catalog

# お問い合わせ（管理者認証も有効化。無効化してもデータは保持）
add-contact:
	go run ./cmd/kit add-contact

remove-contact:
	go run ./cmd/kit remove-contact

# 固定ページ（管理者認証も有効化。無効化してもデータは保持）
add-pages:
	go run ./cmd/kit add-pages

remove-pages:
	go run ./cmd/kit remove-pages
