.PHONY: all build run test test-ui lint sbom verify vuln hooks clean add-site remove-site add-auth remove-auth add-news remove-news auth-setup auth-recover add-catalog remove-catalog add-contact remove-contact add-pages remove-pages add-media remove-media add-documents remove-documents add-mcp remove-mcp add-oidcop remove-oidcop add-federatedauth remove-federatedauth add-analytics remove-analytics records-inventory add-records remove-records

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

# 静的解析（SAST）。道具は版を固定して実行し、go.mod には足さない。版の更新は skills/minimal-web-kit-vuln の「開発とCI」。
# 行ごとの除外は、規則の番号と理由が要る（staticcheck は //lint:ignore <番号> <理由>、gosec は #nosec <番号> -- <理由>）。
# 規則ごと外すときは、ここか staticcheck.conf に理由を書く。test/lint_suppress_test.go が確かめる。
# gosec の G101: 名前（token・secret 等）から秘密値の直書きを推測する規則で、このキットでは画面の文言の表しか当たらない。
#   秘密値の混入は、GitHubのシークレットスキャンとプッシュの保護で止める。
STATICCHECK := go run honnef.co/go/tools/cmd/staticcheck@v0.8.2
GOSEC := go run github.com/securego/gosec/v2/cmd/gosec@v2.30.0

# ソフトウェア部品表（SBOM、CycloneDX のJSON）を、ビルドした実行ファイルから作る。道具は版を固定して go run で動かす。
# 有効な機能で組み込まれる部品が変わるので、ソースではなく、配備する実行ファイルそのものから作る（SBOM_BIN で指定できる）。
# 同じ実行ファイルからは同じ中身になるよう、通し番号と時刻を入れない。htmx とSQLite本体は入らない（skills/minimal-web-kit-vuln）。
CYCLONEDX := go run github.com/CycloneDX/cyclonedx-gomod/cmd/cyclonedx-gomod@v1.12.0
SBOM_BIN ?= bin/$(BIN_NAME)

sbom:
	@test -x "$(SBOM_BIN)" || { echo "$(SBOM_BIN) がありません。先に make build を実行してください" >&2; exit 1; }
	$(CYCLONEDX) bin -json -noserial -notimestamp -output "$(SBOM_BIN).cdx.json" "$(SBOM_BIN)"
	@echo "$(SBOM_BIN).cdx.json"

lint:
	$(STATICCHECK) ./...
	cd test/browser && $(STATICCHECK) ./...
	$(GOSEC) -quiet -nosec-require-rules -nosec-require-justification -exclude=G101 -exclude-dir=.worktrees ./...

# 既知の脆弱性を調べる（道具は版を固定して実行し、go.mod には足さない）。手順は skills/minimal-web-kit-vuln
# test/browser はファイルが全部テストなので、ソースの検査では依存を見ない。テストの実行ファイルを作って検査する。
# 片方が失敗しても、もう片方を実行し、最後に失敗を返す。
GOVULNCHECK := go run golang.org/x/vuln/cmd/govulncheck@v1.9.0

vuln:
	@status=0; \
	$(GOVULNCHECK) -show version ./... || status=1; \
	tmp=$$(mktemp -d); \
	if (cd test/browser && go test -c -o "$$tmp/browser.test" .) && go version -m "$$tmp/browser.test" | grep -q chromedp/chromedp; then \
		(cd test/browser && $(GOVULNCHECK) -show version -mode=binary "$$tmp/browser.test") || status=1; \
	else \
		echo "test/browser could not be scanned" >&2; status=1; \
	fi; \
	rm -rf "$$tmp"; \
	exit $$status

# プッシュの前に、Goが古ければ警告する（プッシュは止めない。クローンごとに1回実行する）
hooks:
	git config core.hooksPath .githooks

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

# 画像（管理者認証も有効化。無効化してもデータは保持）
add-media:
	go run ./cmd/kit add-media

remove-media:
	go run ./cmd/kit remove-media

add-analytics:
	go run ./cmd/kit add-analytics

remove-analytics:
	go run ./cmd/kit remove-analytics

add-records:
	go run ./cmd/kit add-records

remove-records:
	go run ./cmd/kit remove-records

records-inventory:
	go run ./cmd/recordsinventory $(if $(RESET),-reset)

add-documents:
	go run ./cmd/kit add-documents

remove-documents:
	go run ./cmd/kit remove-documents

add-mcp:
	go run ./cmd/kit add-mcp

remove-mcp:
	go run ./cmd/kit remove-mcp
