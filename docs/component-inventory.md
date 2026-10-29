# 共通部品の棚卸し（2026-10-30）

利用者の依頼「共通部品があるのに使っていない箇所、共通部品にできるのにしていない箇所を洗い出す」による調査の結果。コミット `a28c672` の時点のコードを対象にした（行番号はその時点）。外部レビュー（Codex、GPT 6.1 Sol）の指摘を反映した（7節）。

- **A**: 既にある共通部品を使っていない箇所。
- **B**: 2つ以上の機能で同じものを書いていて、共通部品にできる箇所。
- 価値は「直す箇所の数」と「揃わないことで起きる不具合の芽」、リスクは「挙動が変わる範囲」と「守り（認証・CSRF・トランザクション）に触れるか」で見た。

前提: 部品は `docs/components.md`。直近で、カタログの公開一覧の絞り込みを `field`・`choices` に寄せ、`fieldset` の見た目をキット全体で決めた（`a28c672`）。

## 1. すぐ効き、挙動がほぼ変わらないもの

（レビューを受けて、#1・#15・#18・l は見た目や振る舞いが少し変わるため、2節の扱い（確かめてから変える）にした。表には残す。）

| # | 種 | 場所 | 重複・ずれ | 案 |
|---|---|---|---|---|
| 1 | A | `features/catalog/catalog.go:285-290` | 段落の分け方を手で書いている（`form.Paragraphs` と `\n` の削り方が少し違う） | `form.Paragraphs` に置き換える |
| 2 | B | `features/news/views/news_show.html:5-9`、`features/catalog/views/show.html:5`、`features/contact/views/admin_message.html:15` | 段落の描き方（`range` で `p` と `br`）が3回。カタログは別機能のクラス `news-body` を借りている | 部品 `paragraphs` と、汎用のクラス（#3） |
| 3 | B | `style.css` の `.news-body p, .contact-body p` と `.richtext p` | 規則が同じ | 汎用のクラス1つにまとめる |
| 5 | B | `features/catalog/catalog.go:65-90`、`features/contact/admin.go:30-42`・`public.go:29-48`、`features/news/admin.go:18-30`・`public.go:36-56`、`features/pages/admin.go:33-44` | テンプレートを名前の一覧から解析するループが9回（catalog 2、contact 3、news 3、pages 1。auth・Coreにも似たもの） | `web.ParsePages(layout, fsys, names...)`。戻り値のキー（ファイル名）と `views/` を補う規則を明記する |
| 6 | B | `features/catalog/store.go:145-157`（`changed`）、`features/contact/store.go:200-210`（`changed`）、`features/news/posts.go:265-278`（`execOne`）、`features/auth/store.go:73-85`（`affectedOne`）、`features/pages/store.go:180-184,205,216` | 「更新した行が0なら NotFound」。**固定ページの205・216行は `RowsAffected` のエラーを捨てている** | `internal/store.AffectedOne(res, err, ifNone)` |
| 9 | B | `features/auth/handlers.go:652-661`、`features/news/public.go:192-201` | `writeJSON` が同一（ログの文言だけ違う） | `web.WriteJSON` |
| 10 | B | `features/news/admin.go:44-46`、`features/auth/handlers.go:64-66`、`features/contact/*`、`features/pages/*` | ログのリクエストIDの属性を毎回組み立てている。カタログの失敗のログには付いていない | `web.RequestIDAttr(w)` |
| 11 | B | `features/catalog/admin.go:36-41`、`features/news/admin_pages.go:102-107`、`features/contact/public.go:64-71`、`features/pages/admin.go:116-123` | 欄のエラーの文言を表から引く処理（`fieldError`）が4つ | `form.FieldErrors` 型 |
| 14 | B | 7つのレイアウト（`internal/web/views/layout.html`、`features/{site,news,contact,pages,catalog}/views/layout.html`、`features/auth/views/admin_layout.html`） | `<head>` のアイコン3つとCSSの4行 | 部品 `head-assets` |
| 15 | B | 12ファイル22か所（例: `features/news/views/admin_posts.html:23-24`、`features/auth/views/admin_account.html:12,14,15,61`） | `<time datetime=…>` の書き方。形式がそろっていない（`features/catalog/views/show.html:6` は秒とタイムゾーンつき、`features/contact/views/admin_message_delete.html:4` は `<time>` を使っていない） | 部品 `datetime`・`date` |
| 16 | B | `features/auth/views/admin_account.html:80`、`admin_accounts.html:39` | `token-state` の定義が2ファイルに同じ内容 | auth の共通のテンプレートに1つ |
| 18 | B | `style.css` の `.catalog-items` と `.news-list` | カードの縦並び | 汎用の `.card-list` |
| 19 | — | `style.css` の `.admin-caption` と `.richtext caption` | CSSの宣言の重複（部品の未使用ではない） | セレクタをまとめる |
| 22 | — | テストの小さな補助（`itoa`・`env`・`mustExec` 等）が複数のテストにある | 重複だが1行程度 | 共通化しない（5節） |
| l | A | `style.css` の `.combobox-check`、`components.html` の combobox のJSなし用の一覧 | 新しい `.choice` と同じ役目。空のときの文も `Choices.Empty` と同じ | `.choice` と `choices` の形に寄せる |

## 2. 効果が大きいが、挙動の確認が要るもの

| # | 種 | 場所 | 内容 | 案と注意 |
|---|---|---|---|---|
| 4 | B | 描画10か所（`internal/web/handler.go:298-323`、`features/site/site.go:186-197`、`features/news/public.go:59-71`・`admin.go:48-62`、`features/contact/admin.go:61-71`・`public.go:100-111`、`features/pages/admin.go:65-75`・`pages.go:184-195`、`features/catalog/catalog.go:91-113`、`features/auth/handlers.go:68-85`） | バッファに描いて、失敗なら500、成功ならヘッダーを付けて書き出す処理。違いは、レイアウトのデータを足すか、スクリプトを許すCSP、状態コードだけ | 共通にするのは「バッファに描いてHTMLを送る」までに限る。レイアウトのデータ、CSP（auth の `authPageCSP` と catalog の `AllowScripts` は別物）、キャッシュ（news は描く前に公開キャッシュを設定）、テンプレートが無いときの検査（Coreの `Render`）は呼び出し側に残す。失敗のログを誰が出すか（ロガーを渡すか、エラーを返すか）を決める |
| 7 | B | `features/contact/admin.go:73-84`・`features/pages/admin.go:77-88`（`failed`）、`features/catalog/catalog.go:118-131`（`result`）、news はその場で約8回 | エラーを応答に振り分ける処理（NotFound は404、他は500とログ）。例外がある（eの固定ページの404、カタログの入力を残す503、JSONの応答） | 共通にするのは「エラーの分類」まで。応答（入力を残す、JSON、404にするか）は画面・APIの側に残す |
| 8 | B | `features/contact/admin.go:86-93`、`features/pages/admin.go:90-97`、`features/auth/accounts_pages.go:50-57`、`features/catalog/catalog.go:132-145`、`features/news/public.go:74-83`・`admin_pages.go:36-43` | 経路の `{id}` の読み方が3通り。`strconv.ParseInt` を直接使う機能は `+1` を通す。先頭ゼロ（`01`）は数字だけを受け付ける catalog・news でも通る。news は「数字でない」を400、「範囲外」を404に分けている。（`features/pages/admin.go:214` 付近はPOST本文のIDで、別の扱い） | `web.PathID(w, r, "id")`（数字だけ）。`+1`・先頭ゼロ・400と404の区別を、表にして決めてから |
| 12 | B | `features/news/posts.go:29-32`、`features/catalog/store.go:27-30`（`ErrInvalidInput` への `Unwrap` あり）、`features/contact/store.go:33-35`、`features/pages/store.go:29-31`（`ErrInvalidInput` 自体が無く、`errors.As` で判定） | 欄を示す入力のエラーの型が4つ。**エラーの公開の約束が機能で違う** | 統一すると機能ごとの識別を失いうるため、単なる重複除去として扱わない。まず今の約束を残す薄い包みで |
| 13 | B | `features/{news,contact,pages,catalog}/views/layout.html` | 公開ページの自前のレイアウトが4つ（contact と pages は同一）。ナビを固定で書いているため、Siteが無い構成では他の機能のリンクが出ない | Coreに既定の公開レイアウトを1つ置き（`NavLinks` と `page_head` を持つ）、機能に渡す。`test/layout_test.go` に影響 |
| 20 | B | テストの準備（`features/{contact,pages,catalog}/*_test.go`、`test/{admin_ui,news_canary,layout,contact_link,fallback}_test.go`） | 「環境変数→DB→登録→Init→経路」を8回書いている | まとめすぎると結合が強くなる。「DBの準備」「任意のヘッダーを付けた要求」「機能ごとの準備」を分ける。`authtest` に auth を作る処理を入れると、auth 自身のテストから使えなくなる |
| 21 | B | `features/{contact,pages,catalog}/*_test.go`、`features/news/admin_pages_test.go`、`features/auth/fixture_test.go` | 管理画面へのPOSTの再現（同一オリジンのヘッダー、CSRF） | 補助が常に正しいヘッダーとCSRFを補うと、拒否を確かめるテストが弱くなる。ヘッダーを呼び出し側で選べる形にする（auth の今の `postForm` はヘッダーの欠落を再現している） |

## 3. 守り（認証・CSRF・DB）に触れるもの

`minimal-web-kit-audit` の観点で確かめてから進める。

| # | 種 | 場所 | 内容 | 案と注意 |
|---|---|---|---|---|
| 17 | A | `internal/web/session.go:45-58`、`internal/web/guard/guard.go:44-55` | CSRFトークンを作る・確かめる処理（HMAC-SHA256）が同一 | Coreのセッションの中で `guard.CSRF` を使う。挙動が同じことをテストで固めてから |
| e | A/B | `features/catalog/store.go:461-465`（`busyError`。BUSY（5）とLOCKED（6）。catalog だけ） | SQLiteの書き込みの競合は全機能で起こりうる。カタログは保存のときに入力を残したフォームを503で返し（`features/catalog/admin.go:202`）、`result` は汎用の503。他の機能は500。**固定ページの公開ページは、DBの取得の失敗をログに出して404にしている**（`features/pages/pages.go:162`） | 判定（`busyError`）は `internal/store` に移して共有する。応答（入力を残すか、汎用の503か、404か）は画面ごとに決める |
| f | B | `features/auth/accounts.go:62-64`、`features/auth/store.go:205`、`features/pages/store.go:189-194` | UNIQUE違反を、エラーの文字列に "UNIQUE" が含まれるかで判定している | busy と同じく、SQLiteのエラーコードで判定する |
| g | B | `features/catalog/store.go:129-143`（`BEGIN IMMEDIATE`）と、auth の `BeginTx` 約10か所 | トランザクションの始め方が違う | まとめるかは慎重に。auth は今のままでもよい |
| v | B | `features/catalog/store.go:81`（`line`）と `internal/web/form/text.go:44`（`form.Line`） | 1行の入力の検査が機能で違う。catalog は `form.Line` の後に制御文字（タブを含む）も拒否する。`form.Line` は行の中のタブを許すため、news・pages のタイトル、auth の呼び名・端末名などとは受け付ける値が違う（攻撃につながるかは確かめていない） | タブを許すかを決めて、`form.Line` にそろえる |
| w | — | `internal/web/middleware.go:153`（Coreの `CSRFProtect`）と `internal/web/guard/guard.go:22`（`guard.SameOrigin`） | 要求の検査の方針が違う。Coreは `Sec-Fetch-Site: cross-site` を拒否するが、ヘッダーの欠落を許し、Originを比べない。CSRFトークンを本文かヘッダーで受ける。独立経路（管理画面・お問い合わせ）は `same-origin` を必須にし、Originも比べ、トークンは本文だけ | 使う経路（匿名セッションか、管理画面か）が違うため、一律にそろえる根拠にはならない。共通化（#17）の前に、今の違いを仕様として記録し、残すか変えるかを決める。迂回できることを確かめた指摘ではない |

## 4. 判断が要るもの

| # | 場所 | 内容と迷う理由 |
|---|---|---|
| a | `internal/web/views/confirm_delete.html:11-17` | `confirm-actions` と同じ形を手で書いている。戻るの文言が「キャンセルして戻る」で、部品に文言を変える項目が無い。文言を「削除せずに戻る」にそろえれば部品を使える |
| b | `features/{catalog,pages}/views/admin_*form.html`、`features/news/views/admin_post_form.html:8-11` | 「状態: 公開中（公開ページを見る）／下書き」の行。news は予約公開で分岐が多い |
| c | 同じ3ファイル | 公開・非公開のフォームと削除の並び。news は予約公開で形が違う。部品にするなら `form.Publish` |
| d | `features/catalog/admin.go:219-235`、`features/pages/admin.go:235-252`、`features/news/admin_pages.go:232-251` | 公開・非公開の処理。前後の処理（固定ページのナビの作り直し）とストアが違う |
| h | 1ボタンのPOSTのフォーム（`features/auth/views/admin_account.html:32-35` 等） | 部品にできるが引数が多い |
| i | 削除のリンク（`btn btn-danger`）6か所 | 1行で文言も違い、効果は小さい |
| k | 「戻る」リンク | 文言が `docs/components.md` 5節とずれている所がある（`features/catalog/views/admin_form.html:16`・`admin_classifications.html:30`「カタログの管理へ戻る」、`features/news/views/news_show.html:12`・`features/catalog/views/show.html:7`） |
| m | `features/auth/handlers.go:327-335`、`features/catalog/catalog.go:322-331` | 埋め込んだJSを配る処理。JSの承認範囲に関わるので、共通化は慎重に |
| n | `features/news/admin.go:38-42`、`features/catalog/admin.go:93-97` | 画面のデータの薄い包み（時計の差し替え、完了メッセージ） |
| r | `cmd/kit/main.go` | 機能ごとの `add`・`remove` と `removeAuth` の確認が同じ形の繰り返し。表で回せる |
| s | `style.css` の `.cta-banner`・`.cta-actions` | どのテンプレートでも使っていない。利用者向けに残すか消すか |
| t | `internal/web/views/privacy.html:18-44` | セッションの表を手書き。`.Notices` の表と同じ形で描ける |

## 5. 共通化しないもの（勧めない）

- 空の状態の `<p>`（7か所）と管理画面の表（9つ）: 列や文言が画面ごとに違う。形は `test/admin_ui_test.go`・`test/template_test.go` が検査している。
- `LogValue` の1行メソッド、CSRFの hidden の1行、保存日数の計算の書き方の違い、リダイレクト先のURLの組み立て: 小さく、共通化の効果が無い。

## 6. 進め方（レビューを受けて改めた）

変更は**1つの項目（または近い数項目）ごとに分けて**コミットし、原因を切り分けられるようにする。各変更の回帰の検査に、CSPの適用範囲、秘密を含むエラーを出さないこと、IDの受け付けの表、競合のときに入力を残すことを含める。

1. **#6**（固定ページの `RowsAffected` のエラーを捨てている）。潜在的な誤った分類として先に直す（実際の障害は確かめていない）。
2. **1節の残り**（#2・#3・#5・#9・#10・#11・#14・#16・#19）。挙動を変えない共通化。
3. **確かめてから変えるもの**を1つずつ: #1（段落の分け方）、#15（日時の表示の型を少数に決める）、#18・l（見た目の違いを確かめる。combobox のJSなしの一覧は `data-combobox-fallback` との対応を保つ）。
4. **決めてから変えるもの**（利用者と決める）: #8（IDの受け付け）、e（応答）・固定ページのDB障害の404、v（`form.Line` でタブを許すか）、a・k（文言）。
5. **#4・#7**は、それぞれ別の回に。#4 は「描いて送る」まで、#7 は「分類」までを共通にする。
6. **#13**（公開のレイアウト）は、Registry と `BuildRoutes` から既定のレイアウトとナビを渡す形で、別の回に。
7. **3節**（#17・f・g・w）は、今の振る舞いをテストで固めてから。w は仕様として記録するところから。

## 7. 外部レビューの反映（2026-10-30、Codex・GPT 6.1 Sol）

| 重要度 | 指摘 | 反映 |
|---|---|---|
| 高 | エラーの応答の整理に例外がある（固定ページの公開ページはDB障害を404、カタログの保存は入力を残す503） | e・#7 を直し、「分類」と「応答」を分けた |
| 高 | 描画の違いの説明が足りない（auth の独自CSPと catalog の `AllowScripts` は別物、キャッシュ、テンプレートの検査） | #4 の共通化の範囲を「描いて送る」までに限った |
| 中 | 1行の入力の検査が機能で違う（catalog はタブ等の制御文字も拒否） | v として追加 |
| 中 | CSRF・Originの検査の方針の違いが記録されていない | w として追加（仕様として記録してから判断） |
| 中 | contact・pages には `ErrInvalidInput` 自体が無い | #12 の説明を「公開の約束が違う」に直した |
| 中 | テストの補助をまとめると結合が強くなり、拒否のテストが弱くなる | #20・#21 の案を分け方に直し、#22 は共通化しないにした |
| 中 | #1・#15・#18・l は挙動・見た目が変わる | 2節の扱いに移した |
| 中 | まとめて変えると原因を切り分けにくい | 6節を1項目ずつの順に改めた |
| 低 | ループの数（9）、#8 のPOST本文のID、先頭ゼロの扱い、`busyError` の LOCKED、#19 の分類 | 直した |

## 8. 進み具合（2026-10-30）

6節の1〜4を済ませた。利用者の決定: IDは半角の数字だけ（先頭のゼロ・符号・数字でないものは404。お知らせの400も404に）、固定ページの公開ページの取得の失敗は500、`form.Line` は全機能でタブを拒否、文言は「削除せずに戻る」「〜の一覧へ戻る」。

| 項目 | 結果 |
|---|---|
| #6 | `store.AffectedOne`。固定ページで捨てていたエラーを返すようにした |
| #2・#3・#19 | 部品 `paragraphs` と `.text-body`。表の説明のCSSを1つに |
| #5・#9・#10・#11 | `web.ParsePages`・`web.WriteJSON`・`web.RequestIDAttr`・`form.FieldErrors` |
| #14 | 既に `static-head` で済んでいた |
| #16 | `features/auth/views/account_parts.html` に1つ |
| #1 | `form.Paragraphs` に。段落の間に改行が3つ以上あると、次の段落が改行から始まっていたのが直った |
| #15 | 部品 `datetime`・`date`。登録URLの期限の短い表示だけは画面で書く |
| #18・l | `.card-list`。combobox のJSなし用の一覧は `choices` と同じ形（`.choice`） |
| #8 | `form.ID`・`web.PathID` |
| e | `store.Busy`。固定ページの公開ページの取得の失敗は500 |
| v | `form.Line` でタブを拒否。カタログだけの追加の検査をなくした。タブを含む既存のタイトル等は、次に保存するときに直す必要がある |
| a・k | セッション破棄の確認を `confirm-actions` に。戻るの文言をそろえた |

残り: #4・#7・#13（それぞれ別の回）、3節（#17・f・g・w。今の振る舞いをテストで固めてから）、#12・#20・#21、4節の b〜t。
