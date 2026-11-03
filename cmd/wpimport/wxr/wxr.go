// Package wxr は、WordPressの書き出し（WXR）を、移行に要る要素だけ読みます。
// 作者・コメント・許可していないpostmeta等は、値を持たずに読み飛ばします（計画 docs/plans/wordpress-import-plan.md の10.5・10.9）。
// 標準の encoding/xml だけを使います。
package wxr

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Term は、記事に付いたカテゴリです（domain="category" のものだけ）。
type Term struct {
	Nicename string // スラッグ
	Name     string
}

// Item は、WXRの item 1つの、移行に使う値です。
type Item struct {
	ID            int64
	Type          string // post・page・attachment 等
	Status        string // publish・future・draft・pending・private・trash・inherit 等
	Title         string
	Link          string
	Slug          string // post_name
	Content       string // content:encoded（本文のHTML）
	DateGMT       string // post_date_gmt（"2006-01-02 15:04:05"。下書きは "0000-00-00 00:00:00" のことがある）
	Parent        int64
	MenuOrder     int
	Protected     bool // パスワード保護（post_password が空でない）。パスワード自体は持たない
	AttachmentURL string
	Categories    []Term
	Tags          int               // 付いていたタグの数（名前は持たない）
	Meta          map[string]string // allowedMeta のキーだけ
}

// allowedMeta は、読むpostmetaのキーです。それ以外のキーは、値を読まずに捨てます。
var allowedMeta = map[string]bool{
	"_thumbnail_id":                 true,
	"_wp_attached_file":             true,
	"_wp_attachment_image_alt":      true,
	"_yoast_wpseo_primary_category": true,
}

// Export は、WXRから読んだ内容です。
type Export struct {
	BaseSiteURL string
	BaseBlogURL string
	Items       []Item
}

// ErrMalformed は、XMLとして読めないことを示します。文言にファイルの中身を含めません。
var ErrMalformed = errors.New("wxr: malformed XML")

// Read はWXRを読みます。誤りは ErrMalformed（行の番号だけを添える）で返し、ファイルの中身を含めません。
func Read(r io.Reader) (*Export, error) {
	d := xml.NewDecoder(r)
	d.Strict = true
	exp := &Export{}
	inChannel, sawChannel := false, false
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			if !sawChannel {
				return nil, malformed(d) // WXRの形（rss の中の channel）が無い
			}
			break
		}
		if err != nil {
			return nil, malformed(d)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch {
			case t.Name.Local == "channel":
				inChannel, sawChannel = true, true
			case !inChannel:
			case t.Name.Local == "item":
				it, err := readItem(d)
				if err != nil {
					return nil, malformed(d)
				}
				exp.Items = append(exp.Items, it)
			case isWP(t.Name) && t.Name.Local == "base_site_url":
				if exp.BaseSiteURL, err = text(d); err != nil {
					return nil, malformed(d)
				}
			case isWP(t.Name) && t.Name.Local == "base_blog_url":
				if exp.BaseBlogURL, err = text(d); err != nil {
					return nil, malformed(d)
				}
			default:
				// 作者（wp:author）・タグやカテゴリの定義・サイトの説明等は読まない
				if err := d.Skip(); err != nil {
					return nil, malformed(d)
				}
			}
		case xml.EndElement:
			if t.Name.Local == "channel" {
				inChannel = false
			}
		}
	}
	return exp, nil
}

func malformed(d *xml.Decoder) error {
	line, _ := d.InputPos()
	return fmt.Errorf("%w (line %d)", ErrMalformed, line)
}

// isWP は、WordPressの名前空間（http・https、版の違いを問わない）の要素かを返します。
func isWP(n xml.Name) bool { return strings.Contains(n.Space, "wordpress.org/export/") }

const contentSpace = "http://purl.org/rss/1.0/modules/content/"

// text は、今の要素の中の文字だけを読み、終わりのタグまで進めます。子の要素は読み飛ばします。
func text(d *xml.Decoder) (string, error) {
	var b strings.Builder
	for {
		tok, err := d.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.CharData:
			b.Write(t)
		case xml.StartElement:
			if err := d.Skip(); err != nil {
				return "", err
			}
		case xml.EndElement:
			return b.String(), nil
		}
	}
}

func readItem(d *xml.Decoder) (Item, error) {
	it := Item{Meta: map[string]string{}}
	var password string
	for {
		tok, err := d.Token()
		if err != nil {
			return it, err
		}
		switch t := tok.(type) {
		case xml.EndElement:
			it.Protected = strings.TrimSpace(password) != ""
			return it, nil
		case xml.StartElement:
			n := t.Name
			var dst *string
			var number *int64
			switch {
			case n.Space == "" && n.Local == "title":
				dst = &it.Title
			case n.Space == "" && n.Local == "link":
				dst = &it.Link
			case n.Space == contentSpace && n.Local == "encoded":
				dst = &it.Content
			case isWP(n) && n.Local == "post_id":
				number = &it.ID
			case isWP(n) && n.Local == "post_type":
				dst = &it.Type
			case isWP(n) && n.Local == "status":
				dst = &it.Status
			case isWP(n) && n.Local == "post_name":
				dst = &it.Slug
			case isWP(n) && n.Local == "post_date_gmt":
				dst = &it.DateGMT
			case isWP(n) && n.Local == "post_parent":
				number = &it.Parent
			case isWP(n) && n.Local == "post_password":
				dst = &password
			case isWP(n) && n.Local == "attachment_url":
				dst = &it.AttachmentURL
			case isWP(n) && n.Local == "menu_order":
				s, err := text(d)
				if err != nil {
					return it, err
				}
				it.MenuOrder, _ = strconv.Atoi(strings.TrimSpace(s))
				continue
			case n.Space == "" && n.Local == "category":
				if err := readCategory(d, t, &it); err != nil {
					return it, err
				}
				continue
			case isWP(n) && n.Local == "postmeta":
				if err := readMeta(d, &it); err != nil {
					return it, err
				}
				continue
			default:
				// コメント（wp:comment）・作者（dc:creator）・抜粋・その他は値を持たずに読み飛ばす
				if err := d.Skip(); err != nil {
					return it, err
				}
				continue
			}
			s, err := text(d)
			if err != nil {
				return it, err
			}
			if number != nil {
				*number, _ = strconv.ParseInt(strings.TrimSpace(s), 10, 64)
			} else {
				*dst = s
			}
		}
	}
}

func readCategory(d *xml.Decoder, t xml.StartElement, it *Item) error {
	var domain, nicename string
	for _, a := range t.Attr {
		switch a.Name.Local {
		case "domain":
			domain = a.Value
		case "nicename":
			nicename = a.Value
		}
	}
	name, err := text(d)
	if err != nil {
		return err
	}
	switch domain {
	case "category":
		it.Categories = append(it.Categories, Term{Nicename: nicename, Name: strings.TrimSpace(name)})
	case "post_tag":
		it.Tags++
	}
	return nil
}

// readMeta は postmeta を読みます。キーが許可したものでなければ、値を読まずに捨てます（WXRではキーが値より先に来る）。
func readMeta(d *xml.Decoder, it *Item) error {
	var key string
	keep := false
	for {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.EndElement:
			return nil
		case xml.StartElement:
			switch {
			case isWP(t.Name) && t.Name.Local == "meta_key":
				if key, err = text(d); err != nil {
					return err
				}
				keep = allowedMeta[key]
			case isWP(t.Name) && t.Name.Local == "meta_value" && keep:
				v, err := text(d)
				if err != nil {
					return err
				}
				it.Meta[key] = v
			default:
				if err := d.Skip(); err != nil {
					return err
				}
			}
		}
	}
}
