package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"

	"minimal-web-kit/cmd/wpimport/convert"
)

// 出力に書いてよい物（計画10.9）: 固定の理由の語、WordPressのID、件数、移行先のIDとURL、取り込んだ記事の題名、
// 取り込んだ記事から外した画像の旧URLと説明。取り込まなかった記事は題名も書かない。作者・コメントは読んでいない。

// noteLabels は、報告に出す理由の語の説明です。知らない語はそのまま出します（固定の語だけが入る）。
var noteLabels = map[string]string{
	"password_protected":       "パスワード保護の記事（保護なしの下書きにした）",
	"date_missing":             "公開日時が無いか読めない（公開日時なしにした）",
	"categories_dropped":       "使わなかったカテゴリ（1記事に1つ）",
	"tags_dropped":             "移さなかったタグ",
	"category_too_long":        "50文字を超えるカテゴリ（付けなかった）",
	"category_invalid":         "使えない名前のカテゴリ（付けなかった）",
	"page_parent_flattened":    "親のある固定ページ（1階層にした）",
	"slug_changed":             "スラッグを page-<ID> に変えた",
	"title_empty":              "題名が空（仮の題名にした）",
	"title_truncated":          "題名を200文字で切った",
	"body_not_imported":        "本文を取り込めなかった（上限・形式。本文は空）",
	"cover_not_imported":       "代表画像を移せなかった（代表画像なし。管理画面で選び直す）",
	"more_tag":                 "続きを読む（<!--more-->）の印",
	"nextpage_tag":             "ページ分割（<!--nextpage-->）の印",
	"pattern_missing":          "書き出しに無い同期パターン",
	"gallery_empty":            "画像の無いギャラリー",
	"nested_list":              "入れ子のリスト（1段にした）",
	"nested_table":             "入れ子の表",
	"image_in_table":           "表の中の画像（説明の文字にした）",
	"image_unresolved":         "結べなかった本文の画像（説明の文字にした）",
	"link_internal_unresolved": "移行先の無い古いサイトへのリンク（そのまま残した）",
	"link_as_text":             "リンクにできないURL（文字（URL）にした）",
	"link_fragment_or_empty":   "ページ内・空のリンク（文字にした）",
	"link_empty_text":          "文字の無いリンク（URLを文字にした）",
	"table_too_large":          "上限を超える表（文字にした）",
	"table_as_text":            "表（お知らせでは文字にした）",
	"table_header_only":        "見出しの行だけの表",
	"empty_heading":            "文字の無い見出し",
	"control_char":             "制御文字（落とした）",
	"unreadable_html":          "読めない本文",
}

var failureLabels = map[string]string{
	"news_body_only":          "お知らせの本文の途中にだけ出る画像（取り込まない）",
	"media_disabled":          "画像機能が無効",
	"no_uploads_dir":          "アップロードのフォルダの指定が無い",
	"file_missing_or_outside": "ファイルが無いか、フォルダの外を指す（-uploads と -uploads-url の指定を確かめる）",
	"file_unreadable":         "ファイルを読めない",
	"too_large":               "8MBを超える",
	"not_jpeg_png":            "中身がJPEG・PNGでない",
	"too_many_pixels":         "画素数が上限を超える",
	"total_limit":             "画像の合計の上限に達した",
	"bad_alt":                 "説明が使えない",
	"save_failed":             "保存できない",
}

func label(m map[string]string, key string) string {
	if v, ok := m[key]; ok {
		return v
	}
	if i := strings.IndexByte(key, ':'); i > 0 {
		switch key[:i] {
		case "dynamic_block":
			return "表示の時に中身を作るブロック（" + key[i+1:] + "）"
		case "dropped":
			return "落とした要素（" + key[i+1:] + "）"
		case "shortcode":
			return "外したショートコード（" + key[i+1:] + "）"
		}
	}
	return key
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// writeReport は、移行の報告をMarkdownで書きます。
func (p *planner) writeReport(w io.Writer, dryRun bool, newOrigin string) {
	pf := func(format string, a ...any) { fmt.Fprintf(w, format, a...) }
	pf("# WordPressからの移行の報告\n\n")
	if dryRun {
		pf("試しの実行です。DBは変えていません（一時の作業DBに取り込んで、消しました）。\n\n")
	}
	pf("取り込んだ記事は、すべて下書きです。管理画面で確かめてから公開してください。本文に個人情報が無いかも、人が確かめてください（作者とコメントは移していません）。\n\n")

	var news, pagesN, failed int
	for _, e := range p.entries {
		switch {
		case !e.imported:
			failed++
		case e.target == convert.News:
			news++
		default:
			pagesN++
		}
	}
	pf("## 件数\n\n| 種類 | 件数 |\n|---|---|\n| お知らせ | %d |\n| 固定ページ | %d |\n| 取り込めなかった記事 | %d |\n", news, pagesN, failed)
	total, imported := 0, 0
	for _, a := range p.attachments {
		total++
		if a.file != "" {
			imported++
		}
	}
	other := 0
	for _, n := range p.attSkipped {
		other += n
	}
	pf("| 画像（JPEG・PNGの添付） | %d枚中%d枚を移した |\n| JPEG・PNG以外の添付 | %d件（移していない） |\n", total, imported, other)
	pf("| フォルダにあって書き出しに無い画像 | %d枚（移していない。小さい版を除く） |\n\n", p.unlisted)

	if len(p.skipped) > 0 {
		pf("## 取り込まなかった物（種類と件数だけ）\n\n| 種類・状態 | 件数 |\n|---|---|\n")
		for _, k := range sortedKeys(p.skipped) {
			pf("| %s | %d |\n", k, p.skipped[k])
		}
		pf("\n")
	}
	if len(p.attSkipped) > 0 {
		pf("## 移さなかった添付（拡張子ごと）\n\n| 拡張子 | 件数 |\n|---|---|\n")
		for _, k := range sortedKeys(p.attSkipped) {
			pf("| %s | %d |\n", k, p.attSkipped[k])
		}
		pf("\n")
	}

	pf("## 記事ごと\n\n| WordPressのID | 種類 | 移行先 | 題名 | 気をつけること |\n|---|---|---|---|---|\n")
	for _, e := range p.entries {
		kind := "お知らせ"
		if e.target == convert.Page {
			kind = "固定ページ"
		}
		dest := "（取り込めなかった: " + e.failure + "）"
		if e.imported {
			dest = e.newPath
		}
		title := e.title
		if !e.imported {
			title = "" // 取り込まなかった記事の題名は書かない
		}
		var notes, blocks []string
		for _, k := range sortedKeys(e.notes) {
			if name, ok := strings.CutPrefix(k, "dynamic_block:"); ok {
				blocks = append(blocks, fmt.Sprintf("%s ×%d", name, e.notes[k]))
				continue
			}
			notes = append(notes, fmt.Sprintf("%s ×%d", label(noteLabels, k), e.notes[k]))
		}
		if len(blocks) > 0 {
			notes = append(notes, "表示の時に中身を作るブロック（"+strings.Join(blocks, "、")+"）")
		}
		pf("| %d | %s | %s | %s | %s |\n", e.item.ID, kind, dest, mdCell(title), mdCell(strings.Join(notes, "、")))
	}
	pf("\n")

	failures := map[string]int{}
	var placeholders []string
	for _, a := range p.attachments {
		if a.failure != "" {
			failures[a.failure]++
		}
		if a.file != "" && a.placehold {
			placeholders = append(placeholders, "/admin/media で "+a.file+"（WordPressの "+strconv.FormatInt(a.item.ID, 10)+"）")
		}
	}
	if len(failures) > 0 {
		pf("## 移せなかった画像\n\n| 理由 | 件数 |\n|---|---|\n")
		for _, k := range sortedKeys(failures) {
			pf("| %s | %d |\n", label(failureLabels, k), failures[k])
		}
		pf("\n")
	}
	if len(placeholders) > 0 {
		sort.Strings(placeholders)
		pf("## 仮の説明の画像（説明を書き直してください）\n\n")
		for _, s := range placeholders {
			pf("- %s\n", s)
		}
		pf("\n")
	}

	pf("## お知らせの本文から外した画像\n\n本文の途中の画像は移していません。画像にしか無い情報があれば、本文に書き足すか、代表画像に差し替えてください。\n\n")
	any := false
	for _, e := range p.entries {
		if !e.imported || len(e.dropped) == 0 {
			continue
		}
		any = true
		pf("### %s（WordPressの %d → %s、%d枚）\n\n", mdCell(e.title), e.item.ID, e.newPath, len(e.dropped))
		for _, img := range e.dropped {
			alt := img.Alt
			if alt == "" {
				alt = img.Caption
			}
			if alt == "" {
				alt = "説明なし"
			}
			pf("- %s（%s）\n", mdCell(img.Src), mdCell(alt))
		}
		pf("\n")
	}
	if !any {
		pf("ありません。\n\n")
	}
	if newOrigin != "" {
		pf("## 転送\n\n転送の対象は、公開・予約公開で保護の無い記事だけです。CSVは、移行先を公開して確かめた後に、Cloudflareの一括転送に登録してください。\n")
	}
}

// mdCell は、Markdownの表のセルを壊す文字を逃がします。
func mdCell(s string) string {
	return strings.NewReplacer("|", `\|`, "\n", " ", "\r", " ").Replace(s)
}

// redirectRows は、Cloudflareの一括転送のCSVの行です（計画10.8）。
// 旧URLはスキームを除いた「ホスト＋パス」、末尾の / の有り無しの両方、古いサイトのホスト（www の有り無し）ごと。
func (p *planner) redirectRows(oldOrigins []string, newOrigin string) [][]string {
	var hosts []string
	seenHost := map[string]bool{}
	for _, o := range oldOrigins {
		if u, err := url.Parse(o); err == nil && u.Host != "" && !seenHost[strings.ToLower(u.Host)] {
			seenHost[strings.ToLower(u.Host)] = true
			hosts = append(hosts, strings.ToLower(u.Host))
		}
	}
	newBase := strings.TrimSuffix(newOrigin, "/")
	var rows [][]string
	seen := map[string]bool{}
	for _, e := range p.entries {
		if !e.imported || !e.redirect {
			continue
		}
		u, err := url.Parse(e.item.Link)
		if err != nil || u.RawQuery != "" || u.Path == "" {
			continue
		}
		target := newBase + e.newPath
		escaped := strings.TrimSuffix(u.EscapedPath(), "/")
		for _, host := range hosts {
			for _, p := range []string{escaped, escaped + "/"} {
				if p == "" {
					continue
				}
				source := host + p
				if seen[source] || "https://"+source == target || "http://"+source == target {
					continue
				}
				seen[source] = true
				rows = append(rows, []string{source, target, "301", "FALSE", "FALSE", "FALSE", "FALSE"})
			}
		}
	}
	// 取り込んだ画像の旧URL（添付のURLと、本文に出た小さい版のURL）を、移行先の画像へ（計画10.6・13.3）。
	// お知らせの本文にしか出ない画像は取り込んでいないので、転送しない
	ids := make([]int64, 0, len(p.attachments))
	for id, a := range p.attachments {
		if a.file != "" {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	for _, id := range ids {
		a := p.attachments[id]
		target := newBase + "/media/" + a.file
		for _, raw := range a.urls {
			u, err := url.Parse(raw)
			if err != nil || u.Path == "" || (u.Host != "" && !p.oldHosts[hostKey(u.Hostname())]) {
				continue
			}
			for _, host := range hosts {
				source := host + u.EscapedPath()
				if u.Host != "" && hostKey(u.Hostname()) != hostKey(host) {
					continue // 古いサイトのホスト（www の有り無し）ごとに出す。別のサイトのホストの行は出さない
				}
				if !seen[source] {
					seen[source] = true
					rows = append(rows, []string{source, target, "301", "FALSE", "FALSE", "FALSE", "FALSE"})
				}
			}
		}
	}
	return rows
}

func writeCSV(w io.Writer, rows [][]string) error {
	cw := csv.NewWriter(w)
	if err := cw.WriteAll(rows); err != nil {
		return err
	}
	return cw.Error()
}

// mapRows は、対応表（WordPressのIDと移行先のIDとURL）です。個人情報も題名も書きません。
func (p *planner) mapRows() [][]string {
	rows := [][]string{{"kind", "wordpress_id", "kit_id", "kit_url"}}
	for _, e := range p.entries {
		if !e.imported {
			continue
		}
		kind := "news"
		if e.target == convert.Page {
			kind = "page"
		}
		rows = append(rows, []string{kind, strconv.FormatInt(e.item.ID, 10), strconv.FormatInt(e.newID, 10), e.newPath})
	}
	ids := make([]int64, 0, len(p.attachments))
	for id, a := range p.attachments {
		if a.file != "" {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	for _, id := range ids {
		rows = append(rows, []string{"image", strconv.FormatInt(id, 10), strings.TrimSuffix(strings.TrimSuffix(p.attachments[id].file, ".jpg"), ".png"), "/media/" + p.attachments[id].file})
	}
	return rows
}
