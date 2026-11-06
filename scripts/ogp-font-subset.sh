#!/bin/sh
# OGPの画像に使うフォントを、JIS X 0208 と CP932 の文字と ASCII に絞ります（開発の時だけ使う。実行には不要）。
# 使い方: scripts/ogp-font-subset.sh <上流の NotoSansJP-Bold.otf> <出力先>
# 要るもの: Python 3 と fontTools。記録は internal/web/ogimage/font/FONT.md。
set -eu
src=$1
out=$2
chars=$(mktemp)
trap 'rm -f "$chars"' EXIT
python3 -I - "$chars" <<'PY'
import sys
# JIS X 0208（EUC-JP）に加えて、Windows の文字の範囲（CP932）の2バイトの文字を全部集める。
# 日本語入力が出す全角チルダ（U+FF5E）・全角ハイフンマイナス（U+FF0D）・丸数字・㈱・髙 等を描けるようにするため
chars = set(chr(c) for c in range(0x20, 0x7f))
for b1 in range(0xa1, 0xff):
    for b2 in range(0xa1, 0xff):
        try:
            chars.add(bytes([b1, b2]).decode("euc_jp"))
        except UnicodeDecodeError:
            pass
for b1 in list(range(0x81, 0xa0)) + list(range(0xe0, 0xfd)):
    for b2 in list(range(0x40, 0x7f)) + list(range(0x80, 0xfd)):
        try:
            c = bytes([b1, b2]).decode("cp932")
        except UnicodeDecodeError:
            continue
        if len(c) == 1 and c.isprintable():
            chars.add(c)
open(sys.argv[1], "w", encoding="utf-8").write("".join(sorted(chars)))
PY
python3 -I -m fontTools.subset "$src" --text-file="$chars" --output-file="$out" \
  --layout-features='' --no-hinting --desubroutinize --name-IDs='*' --name-languages='*'
