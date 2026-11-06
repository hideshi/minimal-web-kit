#!/bin/sh
# OGPの画像に使うフォントを、JIS X 0208 の文字と ASCII に絞ります（開発の時だけ使う。実行には不要）。
# 使い方: scripts/ogp-font-subset.sh <上流の NotoSansJP-Bold.otf> <出力先>
# 要るもの: Python 3 と fontTools。記録は internal/web/ogimage/font/FONT.md。
set -eu
src=$1
out=$2
chars=$(mktemp)
trap 'rm -f "$chars"' EXIT
python3 -I - "$chars" <<'PY'
import sys
chars = set(chr(c) for c in range(0x20, 0x7f))
for b1 in range(0xa1, 0xff):
    for b2 in range(0xa1, 0xff):
        try:
            chars.add(bytes([b1, b2]).decode("euc_jp"))
        except UnicodeDecodeError:
            pass
open(sys.argv[1], "w", encoding="utf-8").write("".join(sorted(chars)))
PY
python3 -I -m fontTools.subset "$src" --text-file="$chars" --output-file="$out" \
  --layout-features='' --no-hinting --desubroutinize --name-IDs='*' --name-languages='*'
