# 検索とAIの読み取りの出典

検索エンジンとAIに向けた決まり（`skills/minimal-web-kit-seo`）の根拠です。決まりは、ここに載せた公式の文書から導きます。出典の無い「効くと言われている」やり方は、決まりに入れません。

- 確かめた日: 2026-11-06。各社の公式のページを開いて確かめた。
- 原文は写さず、URL・要点・短い引用を置く。文書は書き換わるので、引用は確かめた日の内容です。
- 見直し: 3か月に1回くらいと、Google の Search Central のブログで AI の機能の発表があったときに、下のURLを読み直し、変わった所だけを直す。直したら、確かめた日を書き換える。

Google の文書の引用は、Google が作成・共有した文書の一部で、[クリエイティブ・コモンズ 表示 4.0](https://creativecommons.org/licenses/by/4.0/) の条件で使っています（文章は CC BY 4.0、コードの例は Apache 2.0。https://developers.google.com/site-policies ）。

## 1. 検索とAIの機能に出るための基本（Google）

| 要点 | 出典と引用 |
|---|---|
| AI による概要（AI Overviews）とAIモードに出るのに、特別な対策は要らない。検索の基本がそのまま効く | [AI features and your website](https://developers.google.com/search/docs/appearance/ai-features): "There are no additional requirements to appear in AI Overviews or AI Mode, nor other special optimizations necessary." |
| 出る条件は、検索に載り、説明文（スニペット）つきで表示できること | 同上: "indexed and eligible to be shown in Google Search with a snippet." |
| Search Console で、サイトを「Search の生成AIの機能」に含めていることも条件 | [Optimizing your website for generative AI features on Google Search](https://developers.google.com/search/docs/fundamentals/ai-optimization-guide): "a site must be included in Search generative AI features in Search Console to be eligible" |
| AI向けの最適化は、検索の最適化（SEO）と同じもの | 同上: "optimizing for generative AI search is optimizing for the search experience, and thus still SEO." |
| 一番効くのは、独自で役に立つ中身 | 同上: "Creating content that people find unique, compelling, and useful will likely influence your website's presence in generative AI search in the long run more than any of the other suggestions in this guide." |
| 誰でも書ける一般論（コモディティ）より、自分たちの経験や一次情報を書く | 同上: "Don't just recycle what others on the internet have already said, or could easily be produced by a generative AI model." |
| 読む人に向けて、分かりやすく書く | 同上: "Write content for your human audience and make sure the content is well written and easy to follow." |
| 巡回できること、意味のあるHTML、良いページの体験 | 同上: "ensure your content is crawlable" / "use semantic HTML when possible" / "Provide a good page experience" |
| 大事な中身は文字で置く | [AI features and your website](https://developers.google.com/search/docs/appearance/ai-features) の基本の一覧: "Making sure that important content is available in textual form." |
| 生成AIの機能からの流入は、Search Console の生成AIのレポートで見る | [AI最適化ガイド](https://developers.google.com/search/docs/fundamentals/ai-optimization-guide): "use the Generative AI performance report in Search Console." |
| 出し方を絞るのは、`nosnippet`・`data-nosnippet`・`max-snippet`・`noindex` | [AI features and your website](https://developers.google.com/search/docs/appearance/ai-features) |

## 2. やらなくてよいこと（Google が明言）

決まりに入れない、おまじないの一覧です。

| やらなくてよいこと | 出典と引用 |
|---|---|
| `llms.txt` など AI向けのファイル・マークアップ・Markdown を作る | [AI最適化ガイド](https://developers.google.com/search/docs/fundamentals/ai-optimization-guide): "You don't need to create new machine readable files, AI text files, markup, or Markdown to appear in Google Search" / "Google Search ignores them." |
| AIのために中身を細かく分ける、理想の長さに合わせる | 同上: "There's no requirement to break your content into tiny pieces for AI to better understand it." / "There's no ideal page length." |
| AIのためだけに書き方を変える | 同上: "You don't need to write in a specific way just for generative AI search." |
| 構造化データを AI のために足す | 同上: "Structured data isn't required for generative AI search, and there's no special schema.org markup you need to add." |
| 不自然な言及（被リンク・名前の掲載）を集める | 同上: "seeking inauthentic 'mentions' across the web isn't as helpful as it might seem." |
| 検索の語の言い換えごとにページを作る | 同上: 順位やAIの答えを操作する目的なら "violates Google's scaled content abuse spam policy." |
| 順位を約束する外部の道具を信じる | 同上: "No third-party tool has access to our internal ranking or AI systems." |
| キーワードのメタタグを書く | [SEOスターターガイド](https://developers.google.com/search/docs/fundamentals/seo-starter-guide): "Google Search doesn't use the keywords meta tag." |
| 文字数の目標を決める | 同上: "there's no magical word count target, minimum or maximum" |
| 見出しの順番を検索のために気にする | 同上: "from Google Search perspective, it doesn't matter if you're using them out of order." |
| 重複した中身の罰を恐れる | 同上: 重複は "won't cause a manual action." |

## 3. AIの巡回の名前と、断ると止まるもの

キットの `robots.txt`（`features/site` の `handleRobots`）は、全部を許し、サイトマップの場所を書きます。断りたいサイトは、そのサイトのリポジトリで `handleRobots` を直します。断る前に、下の表で何が止まるかを確かめます。

| 会社 | 名前 | 用途 | 断ると止まるもの | 出典 |
|---|---|---|---|---|
| Google | `Googlebot` | 検索の巡回 | 検索への掲載。AIによる概要・AIモードは検索に載ったページから作られる | [Google の巡回の一覧](https://developers.google.com/search/docs/crawling-indexing/google-common-crawlers) |
| Google | `Google-Extended` | `robots.txt` で使い道を決める名前（巡回の仕組みではない） | Gemini の学習と、Gemini のアプリ・Vertex AI の検索の参照（グラウンディング）。検索への掲載と順位は変わらない: "does not impact a site's inclusion in Google Search nor is it used as a ranking signal" | 同上 |
| Google | `Google-Agent` 等 | 利用者の指示による取得 | 止まらないことがある: "these fetchers generally ignore robots.txt rules" | [利用者の指示で動く取得](https://developers.google.com/crawling/docs/crawlers-fetchers/google-user-triggered-fetchers) |
| Anthropic | `ClaudeBot` | 学習 | 以後の学習に使うこと | [Anthropic の巡回の説明](https://support.claude.com/en/articles/8896518-does-anthropic-crawl-data-from-the-web-and-how-can-site-owners-block-the-crawler) |
| Anthropic | `Claude-SearchBot` | 検索の索引 | Claude の検索の結果に出にくくなる: "may reduce your site's visibility and accuracy in user search results" | 同上 |
| Anthropic | `Claude-User` | 利用者の質問で読む | 利用者の指示による調査で読まれにくくなる | 同上 |
| OpenAI | `GPTBot` | 学習 | 学習に使うこと | [OpenAI の巡回の説明](https://developers.openai.com/api/docs/bots) |
| OpenAI | `OAI-SearchBot` | ChatGPT の検索 | ChatGPT の検索の答えに出なくなる（反映まで約24時間） | 同上 |
| OpenAI | `ChatGPT-User` | 利用者の指示による取得 | 止まらないことがある: "robots.txt rules may not apply" | 同上 |

- Google だけは、学習（Gemini）と、Gemini のアプリでの参照を分けて断れない。学習を断ると、Gemini のアプリで引用されにくくなる。
- 利用者の指示で読みに行く取得（人が AI に調べさせる場合）は、どの会社も `robots.txt` で止まらないことがある。中身を文字で読める作りであることが、そのまま AI に読まれやすさになる。
- 「AIの学習に使わない」を機械で示す共通の仕組み（IETF の aipref の草案）がある。Anthropic は 2026-10-30 から認識し、Google は 2027年の初めに対応するつもりと書いている（各社の上の出典）。
- `llms.txt` は 2024年に Jeremy Howard が出した提案で、標準ではない（https://llmstxt.org/ ）。Google 検索は使わない（2節）。

## 4. 確かめていないこと

- `Google-Extended` を断ったときに、AIによる概要・AIモードに影響するか（文書に書かれていない）。
- `Google-Agent` と Anthropic の `Claude-Agent` の個別のページ。
- Claude の検索が使う索引の提供元。
- `GPTBot`・`OAI-SearchBot` が `robots.txt` に従うことの明記（`robots.txt` で操作する、という書き方だけ）。
- 各ページの最終更新日（取得した本文に日付が無かった）。
