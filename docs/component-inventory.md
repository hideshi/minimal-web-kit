# 共通部品の棚卸し（2026-10-30）

利用者の依頼「共通部品があるのに使っていない箇所、共通部品にできるのにしていない箇所を洗い出す」による調査の結果。コミット `a28c672` の時点のコードを対象にした（行番号はその時点）。外部レビュー前。

- **A**: 既にある共通部品を使っていない箇所。
- **B**: 2つ以上の機能で同じものを書いていて、共通部品にできる箇所。
- 価値は「直す箇所の数」と「揃わないことで起きる不具合の芽」、リスクは「挙動が変わる範囲」と「守り（認証・CSRF・トランザクション）に触れるか」で見た。

前提: 部品は `docs/components.md`。直近で、カタログの公開一覧の絞り込みを `field`・`choices` に寄せ、`fieldset` の見た目をキット全体で決めた（`a28c672`）。

## 1. すぐ効き、挙動がほぼ変わらないもの

| # | 種 | 場所 | 重複・ずれ | 案 |
|---|---|---|---|---|
| 1 | A | `features/catalog/catalog.go:285-290` | 段落の分け方を手で書いている（`form.Paragraphs` と `\n` の削り方が少し違う） | `form.Paragraphs` に置き換える |
| 2 | B | `features/news/views/news_show.html:5-9`、`features/catalog/views/show.html:5`、`features/contact/views/admin_message.html:15` | 段落の描き方（`range` で `p` と `br`）が3回。カタログは別機能のクラス `news-body` を借りている | 部品 `paragraphs` と、汎用のクラス（#3） |
| 3 | B | `style.css` の `.news-body p, .contact-body p` と `.richtext p` | 規則が同じ | 汎用のクラス1つにまとめる |
| 5 | B | `features/catalog/catalog.go:65-90`、`features/contact/admin.go:30-42`・`public.go:29-48`、`features/news/admin.go:18-30`・`public.go:36-56`、`features/pages/admin.go:33-44` | テンプレートを名前の一覧から解析するループが8回 | `web.ParsePages(layout, fsys, names...)` |
| 6 | B | `features/catalog/store.go:145-157`（`changed`）、`features/contact/store.go:200-210`（`changed`）、`features/news/posts.go:265-278`（`execOne`）、`features/auth/store.go:73-85`（`affectedOne`）、`features/pages/store.go:180-184,205,216` | 「更新した行が0なら NotFound」。**固定ページの205・216行は `RowsAffected` のエラーを捨てている** | `internal/store.AffectedOne(res, err, ifNone)` |
| 9 | B | `features/auth/handlers.go:652-661`、`features/news/public.go:192-201` | `writeJSON` が同一（ログの文言だけ違う） | `web.WriteJSON` |
| 10 | B | `features/news/admin.go:44-46`、`features/auth/handlers.go:64-66`、`features/contact/*`、`features/pages/*` | ログのリクエストIDの属性を毎回組み立てている。カタログの失敗のログには付いていない | `web.RequestIDAttr(w)` |
| 11 | B | `features/catalog/admin.go:36-41`、`features/news/admin_pages.go:102-107`、`features/contact/public.go:64-71`、`features/pages/admin.go:116-123` | 欄のエラーの文言を表から引く処理（`fieldError`）が4つ | `form.FieldErrors` 型 |
| 14 | B | 7つのレイアウト（`internal/web/views/layout.html`、`features/{site,news,contact,pages,catalog}/views/layout.html`、`features/auth/views/admin_layout.html`） | `<head>` のアイコン3つとCSSの4行 | 部品 `head-assets` |
| 15 | B | 12ファイル22か所（例: `features/news/views/admin_posts.html:23-24`、`features/auth/views/admin_account.html:12,14,15,61`） | `<time datetime=…>` の書き方。形式がそろっていない（`features/catalog/views/show.html:6` は秒とタイムゾーンつき、`features/contact/views/admin_message_delete.html:4` は `<time>` を使っていない） | 部品 `datetime`・`date` |
| 16 | B | `features/auth/views/admin_account.html:80`、`admin_accounts.html:39` | `token-state` の定義が2ファイルに同じ内容 | auth の共通のテンプレートに1つ |
| 18 | B | `style.css` の `.catalog-items` と `.news-list` | カードの縦並び | 汎用の `.card-list` |
| 19 | A | `style.css` の `.admin-caption` と `.richtext caption` | 規則が同じ | セレクタをまとめる |
| 22 | B | テストの小さな補助（`itoa`・`env`・`mustExec` 等）が複数のテストにある | 重複 | #20 のテスト用パッケージにまとめる |
| l | A | `style.css` の `.combobox-check`、`components.html` の combobox のJSなし用の一覧 | 新しい `.choice` と同じ役目。空のときの文も `Choices.Empty` と同じ | `.choice` と `choices` の形に寄せる |

## 2. 効果が大きいが、挙動の確認が要るもの

| # | 種 | 場所 | 内容 | 案と注意 |
|---|---|---|---|---|
| 4 | B | 描画10か所（`internal/web/handler.go:298-323`、`features/site/site.go:186-197`、`features/news/public.go:59-71`・`admin.go:48-62`、`features/contact/admin.go:61-71`・`public.go:100-111`、`features/pages/admin.go:65-75`・`pages.go:184-195`、`features/catalog/catalog.go:91-113`、`features/auth/handlers.go:68-85`） | バッファに描いて、失敗なら500、成功ならヘッダーを付けて書き出す処理。違いは、レイアウトのデータを足すか、スクリプトを許すCSP、状態コードだけ | `web.WriteHTML(w, status, t, name, data)` と、nil でも安全な `Layout.Apply(data)`。CSPは描画に成功した後に変える今の順を守る |
| 7 | B | `features/contact/admin.go:73-84`・`features/pages/admin.go:77-88`（`failed`）、`features/catalog/catalog.go:118-131`（`result`）、news はその場で約8回 | エラーを応答に振り分ける処理（NotFound は404、他は500とログ） | `web.Fail(w, r, logger, event, err, notFound)`。busy の503（e）も選べる形に |
| 8 | B | `features/contact/admin.go:86-93`、`features/pages/admin.go:90-97,214-219`、`features/auth/accounts_pages.go:50-57`、`features/catalog/catalog.go:132-145`、`features/news/public.go:74-83`・`admin_pages.go:36-43` | 経路の `{id}` の読み方が3通り。`strconv.ParseInt` は `+1` や `01` も通すため、**機能によって受け付けるURLが違う**。news は「数字でない」を400、「範囲外」を404に分けている | `web.PathID(w, r, "id")`（数字だけ）。news の400と404の区別をやめるかを決める |
| 12 | B | `features/news/posts.go:29-32`、`features/catalog/store.go:27-30`（`Unwrap` あり）、`features/contact/store.go:33-35`、`features/pages/store.go:29-31`（`Unwrap` なし） | 欄を示す入力のエラーの型が4つ。`errors.Is(err, ErrInvalidInput)` の振る舞いが機能で違う | `form.InputError` と `form.ErrInvalid` に統一。各機能の判定をテストで確かめる |
| 13 | B | `features/{news,contact,pages,catalog}/views/layout.html` | 公開ページの自前のレイアウトが4つ（contact と pages は同一）。ナビを固定で書いているため、Siteが無い構成では他の機能のリンクが出ない | Coreに既定の公開レイアウトを1つ置き（`NavLinks` と `page_head` を持つ）、機能に渡す。`test/layout_test.go` に影響 |
| 20 | B | テストの準備（`features/{contact,pages,catalog}/*_test.go`、`test/{admin_ui,news_canary,layout,contact_link,fallback}_test.go`） | 「環境変数→DB→登録→Init→経路」を8回書いている | テスト用のパッケージに `Routes(t, features...)`（auth を使うため、auth 自身のテストからは使えない） |
| 21 | B | `features/{contact,pages,catalog}/*_test.go`、`features/news/admin_pages_test.go`、`features/auth/fixture_test.go` | 管理画面へのPOSTの再現（同一オリジンのヘッダー、CSRF） | `authtest.PostForm` |

## 3. 守り（認証・CSRF・DB）に触れるもの

`minimal-web-kit-audit` の観点で確かめてから進める。

| # | 種 | 場所 | 内容 | 案と注意 |
|---|---|---|---|---|
| 17 | A | `internal/web/session.go:45-58`、`internal/web/guard/guard.go:44-55` | CSRFトークンを作る・確かめる処理（HMAC-SHA256）が同一 | Coreのセッションの中で `guard.CSRF` を使う。挙動が同じことをテストで固めてから |
| e | B | `features/catalog/store.go:461-465`（`busyError`。catalog だけ） | SQLiteの書き込みの競合（BUSY）は全機能で起こりうるが、503で返すのはカタログだけ。他は500 | `internal/store` に置いて全機能で使う |
| f | B | `features/auth/accounts.go:62-64`、`features/auth/store.go:205`、`features/pages/store.go:189-194` | UNIQUE違反を、エラーの文字列に "UNIQUE" が含まれるかで判定している | busy と同じく、SQLiteのエラーコードで判定する |
| g | B | `features/catalog/store.go:129-143`（`BEGIN IMMEDIATE`）と、auth の `BeginTx` 約10か所 | トランザクションの始め方が違う | まとめるかは慎重に。auth は今のままでもよい |

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

## 6. 進め方の案

1. **1節**（挙動がほぼ変わらない）をまとめて直す。固定ページの `RowsAffected` のエラーの捨て方（#6）は不具合なので先に。
2. **2節**は #4・#7・#8 をまとめて1回で（呼び出し側がそろう）。#8 で受け付けるURLが変わる点は、利用者に決めてもらう。#13 は公開のレイアウトの作り直しになるので別の回に。
3. **3節**は外部レビューの意見を聞いてから。#17 は挙動を変えないことをテストで固めてから。e・f は不具合の芽なので、2節と一緒に直してよい。
4. **4節**は利用者と決める。a・k（文言のずれ）は1節と一緒に直してよい。
