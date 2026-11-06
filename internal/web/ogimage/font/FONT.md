# OGPの画像のフォント

`NotoSansJP-Bold-subset.otf` は、Noto Sans CJK（日本語）の太字を、OGPの画像に描く文字に絞ったものです。LICENSE（SIL Open Font License 1.1）と一緒に配ります。

| 項目 | 内容 |
|---|---|
| 元のファイル | `notofonts/noto-cjk` のタグ `Sans2.004` の `Sans/SubsetOTF/JP/NotoSansJP-Bold.otf`（https://github.com/notofonts/noto-cjk） |
| 元のファイルの SHA-256 | `1b0edfb500b73a4fa8a4fcaae1bbbd403994e08e73e3e0da37e70d3853f42c5f` |
| ライセンス | SIL Open Font License 1.1（同じタグのリポジトリ直下の `LICENSE` をそのまま `LICENSE` に置いた） |
| 著作権表示 | © 2014-2021 Adobe (http://www.adobe.com/). |
| 予約フォント名 | 無い（`LICENSE` に指定が無い。2026-11-06 に確かめた） |
| 絞った文字 | JIS X 0208 の文字（EUC-JP で表せる2バイトの文字）、Windows の文字の範囲（CP932）の2バイトの文字（全角チルダ・全角ハイフンマイナス・丸数字・㈱・髙 等）、ASCII の印字できる文字。集めた文字は7,427文字で、上流のフォントに無い `≒`（U+2252）等は入らない（フォントの文字は7,426文字） |
| 作り方 | `scripts/ogp-font-subset.sh <元のファイル> NotoSansJP-Bold-subset.otf`（fontTools 4.57.0。レイアウトの機能とヒンティングを外し、名前の表は残す） |
| 出来たファイルの SHA-256 | `174d63310bf7f09f012d0e26feab8c2513b6876935e4fd8ff44c4d542d6e1254`（同じ版の fontTools で作り直すと同じになる） |

- 変更版（サブセット）も OFL のままです。フォントだけを売ることはしません。
- 実行ファイルだけを配る場合は、`minimal-web-kit licenses` で著作権表示とライセンスの全文を出せます。
- フォントを替えると、OGPの画像のURLの版が変わります（`ogimage.Version` がフォントのハッシュを入力に含む）。
