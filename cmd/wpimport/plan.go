package main

import (
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"minimal-web-kit/cmd/wpimport/convert"
	"minimal-web-kit/cmd/wpimport/wxr"
)

// 1段目: 全件の扱い・移行先・スラッグ・画像を、DBに書く前に決める（計画10.1・10.8）。

// attachment は、画像の添付1つの計画です。
type attachment struct {
	item      wxr.Item
	rel       string // アップロードのフォルダの中の相対パス（安全を確かめる前）
	ext       string
	alt       string // 説明（WXRの代替テキスト → 本文の alt → キャプション → 仮の文）
	placehold bool   // 仮の説明か
	usedPage  bool   // 固定ページの本文で使う
	usedCover bool   // お知らせの代表画像に使う
	usedNews  bool   // お知らせの本文に出た（代表画像以外）
	file      string // 取り込んだファイル名
	failure   string // 取り込めなかった理由（固定の語）
	urls      []string
}

// imageExt は、取り込める画像の拡張子です。中身の形式は取り込みの時に画像機能が確かめます。
var imageExt = map[string]bool{"jpg": true, "jpeg": true, "png": true}

// entry は、取り込む記事・固定ページ1つの計画です。
type entry struct {
	item        wxr.Item
	target      convert.Target
	title       string
	slug        string    // 固定ページ
	publishedAt time.Time // お知らせの公開日時、固定ページの公開予約（ゼロ値なら無し）
	redirect    bool      // 転送の対象（公開・予約公開で、保護の無い物）
	category    string    // お知らせのカテゴリの名前
	cover       int64     // お知らせの代表画像（添付のID）
	newID       int64
	newPath     string
	notes       map[string]int
	dropped     []convert.Image // お知らせの本文から外した画像
	imported    bool
	failure     string
}

type planner struct {
	exp          *wxr.Export
	oldHosts     map[string]bool
	uploadsURLs  []string
	attachments  map[int64]*attachment
	byURL        map[string]int64 // 画像のURL（クエリを除く）→ 添付のID
	byPath       map[string]int64 // 画像のURLのパス → 添付のID
	children     map[int64][]int64
	patterns     map[int64]string
	entries      []*entry
	byLink       map[string]*entry // 古いサイトのパス（末尾の / を除く）→ 記事
	byWPID       map[int64]*entry
	skipped      map[string]int // 取り込まない物の種類と状態（題名は持たない）
	attSkipped   map[string]int // 取り込めない添付の拡張子
	unlisted     int            // アップロードのフォルダにあってWXRに無い画像の数
	enabled      enabled
	existingSlug map[string]bool
	checkSlug    func(string) error
}

type enabled struct{ news, pages, media bool }

func newPlanner(exp *wxr.Export, oldOrigins, uploadsURLs []string, en enabled, existingSlug map[string]bool, checkSlug func(string) error) *planner {
	p := &planner{
		exp: exp, oldHosts: map[string]bool{}, uploadsURLs: uploadsURLs, attachments: map[int64]*attachment{}, byURL: map[string]int64{}, byPath: map[string]int64{},
		children: map[int64][]int64{}, patterns: map[int64]string{}, byLink: map[string]*entry{}, byWPID: map[int64]*entry{},
		skipped: map[string]int{}, attSkipped: map[string]int{}, enabled: en, existingSlug: existingSlug, checkSlug: checkSlug,
	}
	for _, o := range oldOrigins {
		if u, err := url.Parse(o); err == nil && u.Host != "" {
			p.oldHosts[hostKey(u.Host)] = true
		}
	}
	return p
}

// hostKey は、古いサイトのホストを比べる形です（小文字、www. の有り無しを同じに扱う）。
func hostKey(h string) string { return strings.TrimPrefix(strings.ToLower(h), "www.") }

// run は1段目を行います。
func (p *planner) run() {
	for _, it := range p.exp.Items {
		switch it.Type {
		case "attachment":
			p.addAttachment(it)
		case "wp_block":
			p.patterns[it.ID] = it.Content
		}
	}
	for parent, ids := range p.children {
		sort.Slice(ids, func(i, j int) bool {
			a, b := p.attachments[ids[i]].item, p.attachments[ids[j]].item
			if a.MenuOrder != b.MenuOrder {
				return a.MenuOrder < b.MenuOrder
			}
			return a.ID < b.ID
		})
		p.children[parent] = ids
	}
	for _, it := range p.exp.Items {
		switch it.Type {
		case "post", "page":
			p.addEntry(it)
		case "attachment", "wp_block":
		default:
			p.skipped["type:"+it.Type]++
		}
	}
	p.decideSlugs()
	// 本文の画像と説明を集める（何も結ばない resolver で1回変換する）
	for _, e := range p.entries {
		res := convert.Convert(e.item.Content, e.item.ID, e.target, collector{p})
		var bodyImages []int64
		for _, img := range res.Images {
			id := p.attachmentFor(img)
			if a := p.attachments[id]; a != nil {
				if a.alt == "" && img.Alt != "" {
					a.alt = img.Alt
				}
				if a.alt == "" && img.Caption != "" {
					a.alt = img.Caption
				}
				a.addURL(img.Src)
			}
			if e.target == convert.Page {
				if a := p.attachments[id]; a != nil {
					a.usedPage = true
				}
				continue
			}
			bodyImages = append(bodyImages, id)
		}
		if e.target == convert.News {
			p.decideCover(e, bodyImages, res.Images)
		}
	}
	for _, a := range p.attachments {
		if a.alt == "" {
			a.alt, a.placehold = convert.PlaceholderAlt, true
		}
		if r := []rune(a.alt); len(r) > 200 {
			a.alt = string(r[:200])
		}
	}
}

func (p *planner) addAttachment(it wxr.Item) {
	if it.ID <= 0 {
		p.attSkipped["no_id"]++ // ID の無い添付は、0（結べない）と区別できないため使わない
		return
	}
	rel := strings.TrimSpace(it.Meta["_wp_attached_file"])
	if rel == "" {
		rel = p.relFromURL(it.AttachmentURL)
	}
	ext := strings.TrimPrefix(strings.ToLower(path.Ext(rel)), ".")
	if ext == "" {
		ext = strings.TrimPrefix(strings.ToLower(path.Ext(stripQuery(it.AttachmentURL))), ".")
	}
	if !imageExt[ext] {
		if ext == "" {
			ext = "unknown"
		}
		p.attSkipped[ext]++
		return
	}
	a := &attachment{item: it, rel: rel, ext: ext, alt: strings.TrimSpace(strings.Join(strings.Fields(it.Meta["_wp_attachment_image_alt"]), " "))}
	a.addURL(it.AttachmentURL)
	for _, base := range p.uploadsURLs {
		if rel != "" {
			a.addURL(base + rel)
		}
	}
	p.attachments[it.ID] = a
	for _, u := range a.urls {
		p.byURL[u] = it.ID
		if parsed, err := url.Parse(u); err == nil && parsed.Path != "" {
			p.byPath[parsed.Path] = it.ID
		}
	}
	if it.Parent > 0 {
		p.children[it.Parent] = append(p.children[it.Parent], it.ID)
	}
}

func (a *attachment) addURL(u string) {
	u = stripQuery(strings.TrimSpace(u))
	if u == "" {
		return
	}
	for _, x := range a.urls {
		if x == u {
			return
		}
	}
	a.urls = append(a.urls, u)
}

func stripQuery(u string) string {
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		return u[:i]
	}
	return u
}

// relFromURL は、添付のURLからアップロードのフォルダの中の相対パスを決めます（_wp_attached_file が無い書き出し。計画13.3）。
func (p *planner) relFromURL(u string) string {
	u = stripQuery(u)
	for _, base := range p.uploadsURLs {
		if strings.HasPrefix(u, base) {
			return strings.TrimPrefix(u, base)
		}
	}
	return ""
}

var reSized = regexp.MustCompile(`-\d+x\d+(\.[A-Za-z]+)$`)

// attachmentFor は、本文の画像を添付に結びます。wp-image-<ID> を最優先に、無ければURL（小さい版は元の名前に戻して）で引きます。
func (p *planner) attachmentFor(img convert.Image) int64 {
	if img.AttachmentID > 0 && p.attachments[img.AttachmentID] != nil {
		return img.AttachmentID
	}
	src := stripQuery(img.Src)
	base := reSized.ReplaceAllString(src, "$1")
	// 大きな画像は添付が -scaled の版のため、小さい版の名前を戻した物に -scaled を足した名前でも引く
	scaled := strings.TrimSuffix(base, path.Ext(base)) + "-scaled" + path.Ext(base)
	for _, s := range []string{src, base, scaled} {
		if id, ok := p.byURL[s]; ok {
			return id
		}
		// ホストの揺れ（www の有り無し・http）と、サイト内のパスでの参照は、古いサイトのパスで引く
		if u, err := url.Parse(s); err == nil && u.Path != "" && (u.Host == "" || p.oldHosts[hostKey(u.Hostname())]) {
			if id, ok := p.byPath[u.Path]; ok {
				return id
			}
		}
	}
	return 0
}

// statusRule は、計画10.5の状態の許可表です。
func statusRule(it wxr.Item) (ok, redirect, dated bool) {
	switch it.Status {
	case "publish", "future":
		return true, !it.Protected, true
	case "draft", "pending":
		return true, false, false
	}
	return false, false, false
}

func (p *planner) addEntry(it wxr.Item) {
	ok, redirect, dated := statusRule(it)
	if !ok {
		p.skipped["status:"+it.Type+"/"+it.Status]++
		return
	}
	target := convert.News
	if it.Type == "page" {
		target = convert.Page
	}
	if (target == convert.News && !p.enabled.news) || (target == convert.Page && !p.enabled.pages) {
		p.skipped["feature_disabled:"+it.Type]++
		return
	}
	e := &entry{item: it, target: target, redirect: redirect, notes: map[string]int{}}
	e.title = cleanTitle(it.Title, it.ID, e.notes)
	if it.Protected {
		e.notes["password_protected"]++
	}
	if dated {
		if t, err := time.ParseInLocation("2006-01-02 15:04:05", it.DateGMT, time.UTC); err == nil && t.Unix() > 0 {
			e.publishedAt = t
		} else {
			e.notes["date_missing"]++
		}
	}
	if target == convert.News {
		e.category = p.category(it, e.notes)
	}
	if it.Parent > 0 && target == convert.Page {
		e.notes["page_parent_flattened"]++
	}
	p.entries = append(p.entries, e)
	p.byWPID[it.ID] = e
	// 本文の中のリンクの書き換えには、取り込む記事を全部使う（保護・下書きも。転送の対象かとは別）
	if u, err := url.Parse(it.Link); err == nil && u.RawQuery == "" && u.Path != "" {
		p.byLink[pathKey(u.Path)] = e
	}
}

// cleanTitle は題名を1行の1〜200文字にします。空なら固定の仮の題名にします。
func cleanTitle(raw string, id int64, notes map[string]int) string {
	t := strings.Join(strings.Fields(raw), " ")
	t = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, t)
	if t == "" {
		notes["title_empty"]++
		return "無題（WordPressの " + strconv.FormatInt(id, 10) + "）"
	}
	if utf8.RuneCountInString(t) > 200 {
		notes["title_truncated"]++
		t = string([]rune(t)[:200])
	}
	return t
}

// category は、お知らせのカテゴリを1つ決めます（計画10.5）。未分類を外し、Yoastの主カテゴリがあればそれ、無ければ最初の物。
func (p *planner) category(it wxr.Item, notes map[string]int) string {
	var names []wxr.Term
	for _, c := range it.Categories {
		if c.Nicename == "uncategorized" || c.Name == "未分類" || c.Name == "" {
			continue
		}
		names = append(names, c)
	}
	if it.Tags > 0 {
		notes["tags_dropped"] += it.Tags
	}
	if len(names) == 0 {
		return ""
	}
	chosen := names[0]
	if primary := it.Meta["_yoast_wpseo_primary_category"]; primary != "" {
		for _, c := range names {
			if c.Nicename == primary || c.Name == primary {
				chosen = c
			}
		}
	}
	if len(names) > 1 {
		notes["categories_dropped"] += len(names) - 1
	}
	name := strings.Join(strings.Fields(chosen.Name), " ")
	if utf8.RuneCountInString(name) > 50 {
		notes["category_too_long"]++
		return ""
	}
	return name
}

// decideSlugs は、固定ページのスラッグを決めます。使えない物・重なる物は page-<WordPressのID> にして報告します（計画10.8）。
func (p *planner) decideSlugs() {
	used := map[string]bool{}
	for s := range p.existingSlug {
		used[s] = true
	}
	for _, e := range p.entries {
		if e.target != convert.Page {
			continue
		}
		slug := strings.ToLower(e.item.Slug)
		if dec, err := url.PathUnescape(slug); err == nil {
			slug = dec
		}
		if p.checkSlug(slug) != nil || used[slug] { // 形・予約語は固定ページの機能の規則で確かめる
			e.notes["slug_changed"]++
			slug = "page-" + strconv.FormatInt(e.item.ID, 10)
			for i := 2; used[slug]; i++ {
				slug = "page-" + strconv.FormatInt(e.item.ID, 10) + "-" + strconv.Itoa(i)
			}
		}
		used[slug] = true
		e.slug = slug
		e.newPath = "/" + slug
	}
}

// decideCover は、お知らせの代表画像を決めます。アイキャッチ、無ければ本文の最初の画像。それ以外の本文の画像は外して報告します。
func (p *planner) decideCover(e *entry, bodyImages []int64, images []convert.Image) {
	if id, err := strconv.ParseInt(e.item.Meta["_thumbnail_id"], 10, 64); err == nil && p.attachments[id] != nil {
		e.cover = id
	}
	for i, id := range bodyImages {
		if e.cover == 0 && id != 0 {
			e.cover = id
			continue
		}
		if id == e.cover && id != 0 {
			continue
		}
		e.dropped = append(e.dropped, images[i])
		if a := p.attachments[id]; a != nil {
			a.usedNews = true
		}
	}
	if a := p.attachments[e.cover]; a != nil {
		a.usedCover = true
	}
}

// wanted は、取り込む画像かを返します。お知らせの本文にしか出ない画像は、取り込んでも公開されないため取り込みません（計画10.6）。
func (a *attachment) wanted() bool {
	return a.usedPage || a.usedCover || !a.usedNews
}

// pathKey は、古いサイトのパスを比べる形です（末尾の / を除き、符号化を戻す）。
func pathKey(p string) string {
	if dec, err := url.PathUnescape(p); err == nil {
		p = dec
	}
	p = strings.TrimSuffix(p, "/")
	if p == "" {
		return "/"
	}
	return p
}

// safePath は、アップロードのフォルダの中のファイルの実際のパスを返します。外を指す物は断ります（計画10.6）。
func safePath(uploads, rel string) (string, bool) {
	rel = filepath.FromSlash(rel)
	if rel == "" || filepath.IsAbs(rel) || !filepath.IsLocal(rel) {
		return "", false
	}
	root, err := filepath.EvalSymlinks(uploads)
	if err != nil {
		return "", false
	}
	full, err := filepath.EvalSymlinks(filepath.Join(root, rel))
	if err != nil {
		return "", false
	}
	if r, err := filepath.Rel(root, full); err != nil || !filepath.IsLocal(r) {
		return "", false
	}
	return full, true
}

// collector は、1段目で本文の画像を集めるための resolver です。何も結ばず、[gallery] と同期パターンだけを引きます。
type collector struct{ p *planner }

func (collector) Image(convert.Image) (string, string, bool) { return "", "", false }
func (collector) Link(h string) (string, bool, bool)         { return h, false, false }
func (c collector) Attachments(id int64) []int64             { return c.p.children[id] }
func (c collector) Pattern(ref int64) (string, bool)         { s, ok := c.p.patterns[ref]; return s, ok }
