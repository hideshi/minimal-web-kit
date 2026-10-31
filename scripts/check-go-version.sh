#!/bin/sh
# go.mod のGoが、同じ系列の最新のパッチ版かを確かめます。
# 終了コード: 0 最新、1 古い、2 確かめられない
set -eu

current=$(sed -n 's/^go \([0-9.]*\)$/\1/p' go.mod)
series=$(echo "$current" | cut -d. -f1,2)
if [ -z "$current" ]; then
	echo "go.mod の go 行を読めません" >&2
	exit 2
fi
if ! list=$(curl -fsS --max-time 15 'https://go.dev/dl/?mode=json'); then
	echo "Goの公開版の一覧を取得できませんでした" >&2
	exit 2
fi
latest=$(echo "$list" | grep -o '"version": "go[0-9.]*"' | sed 's/.*"go\([0-9.]*\)"/\1/' | sort -u | grep -E "^$series(\.|$)" | sort -V | tail -1 || true)
if [ -z "$latest" ]; then
	echo "Go $series は、保守されている版の一覧にありません（go.mod は $current）。主な版を上げてください" >&2
	exit 1
fi
if [ "$current" != "$latest" ]; then
	echo "Goが古い: go.mod は $current、最新は $latest。手順は skills/minimal-web-kit-vuln" >&2
	exit 1
fi
echo "Goは最新のパッチ版です（$current）"
