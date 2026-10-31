#!/bin/sh
# 今の版に当たるGitHubの勧告を調べます。govulncheckのデータベースの遅れを補い、htmxも見ます。
# 呼び出し経路は見ないので、結果は手順に沿って判定します。
# 終了コード: 0 なし、1 あり、2 確かめられない
set -eu

{
	go list -m -f '{{if not .Main}}{{.Path}}@{{.Version}}{{end}}' all
	(cd test/browser && go list -m -f '{{if not .Main}}{{.Path}}@{{.Version}}{{end}}' all)
} | grep -v '^minimal-web-kit' | sort -u >"${TMPDIR:-/tmp}/kit-modules.$$"
trap 'rm -f "${TMPDIR:-/tmp}/kit-modules.$$"' EXIT

htmx=$(sed -n 's/.*htmx \([0-9][0-9.]*\)、.*/\1/p' features/catalog/static/README.md | head -1)
if [ ! -s "${TMPDIR:-/tmp}/kit-modules.$$" ] || [ -z "$htmx" ]; then
	echo "調べる対象を読めません" >&2
	exit 2
fi

found=0
query() {
	if ! out=$(gh api --paginate -X GET /advisories -f ecosystem="$1" -f affects="$2" --jq '.[]|[.ghsa_id,.severity,.published_at[:10],.summary]|@tsv'); then
		echo "勧告を取得できませんでした" >&2
		exit 2
	fi
	if [ -n "$out" ]; then
		echo "$out"
		found=1
	fi
}

# 検索の文字数を抑えるため、40件ずつ送る
tr '\n' ' ' <"${TMPDIR:-/tmp}/kit-modules.$$" | xargs -n 40 | tr ' ' ',' >"${TMPDIR:-/tmp}/kit-modules.$$.batch"
while read -r batch; do
	query go "$batch"
done <"${TMPDIR:-/tmp}/kit-modules.$$.batch"
rm -f "${TMPDIR:-/tmp}/kit-modules.$$.batch"
query npm "htmx.org@$htmx"

if [ "$found" -ne 0 ]; then
	echo "今の版に当たる勧告があります。手順は skills/minimal-web-kit-vuln" >&2
	exit 1
fi
echo "今の版に当たる勧告はありません（$(wc -l <"${TMPDIR:-/tmp}/kit-modules.$$") モジュールと htmx $htmx）"
