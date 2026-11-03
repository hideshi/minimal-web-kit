package wxr

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

func readSample(t *testing.T) *Export {
	t.Helper()
	f, err := os.Open("../testdata/sample.xml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	exp, err := Read(f)
	if err != nil {
		t.Fatal(err)
	}
	return exp
}

func TestRead_Sample(t *testing.T) {
	exp := readSample(t)
	if exp.BaseBlogURL != "https://old.example.com" || len(exp.Items) != 10 {
		t.Fatalf("export: %q %d", exp.BaseBlogURL, len(exp.Items))
	}
	byID := map[int64]Item{}
	for _, it := range exp.Items {
		byID[it.ID] = it
	}
	page := byID[10]
	if page.Type != "page" || page.Status != "publish" || page.Slug != "company" || page.MenuOrder != 2 || page.DateGMT != "2024-05-01 01:00:00" ||
		!strings.Contains(page.Content, `<h2 class="wp-block-heading">私たちについて</h2>`) || page.Link != "https://old.example.com/company/" {
		t.Errorf("page: %+v", page)
	}
	post := byID[20]
	if len(post.Categories) != 3 || post.Categories[1] != (Term{Nicename: "event", Name: "イベント"}) || post.Tags != 1 ||
		post.Meta["_thumbnail_id"] != "30" || post.Meta["_yoast_wpseo_primary_category"] != "shop" {
		t.Errorf("post: %+v", post)
	}
	if !strings.Contains(post.Content, "5月に<strong>新しい店</strong>を開きました。\n\n営業時間") {
		t.Errorf("classic body must keep blank lines: %q", post.Content)
	}
	if byID[21].DateGMT != "0000-00-00 00:00:00" || byID[21].Status != "draft" {
		t.Errorf("draft: %+v", byID[21])
	}
	if !byID[22].Protected || byID[23].Status != "future" || byID[24].Status != "private" || byID[25].Type != "flamingo_inbound" {
		t.Error("status, type or protection")
	}
	att := byID[30]
	if att.Type != "attachment" || att.Parent != 10 || att.Meta["_wp_attached_file"] != "2024/05/shop-scaled.jpg" ||
		att.Meta["_wp_attachment_image_alt"] != "店の外観" || att.AttachmentURL != "https://old.example.com/wp-content/uploads/2024/05/shop-scaled.jpg" {
		t.Errorf("attachment: %+v", att)
	}
}

// TestRead_DropsPersonalData は、作者・コメント・パスワード・許可していないpostmetaの値を持たないことを確かめる。
// 取り込まない投稿タイプの題名は持つが、それを出さないのは使う側の役目（10.9）。
func TestRead_DropsPersonalData(t *testing.T) {
	exp := readSample(t)
	dump := fmt.Sprintf("%+v", *exp)
	for _, canary := range []string{
		"CANARY_AUTHOR", "canary-author@", "CANARY_COMMENT", "canary-comment@", "203.0.113.77", "CANARY_PASSWORD",
		"CANARY_POSTMETA", "CANARY_ATTACHMENT_META", "CANARY_EXCERPT", "CANARY_TAG", "CANARY_SITE_DESCRIPTION",
	} {
		if strings.Contains(dump, canary) {
			t.Errorf("read value must not contain %s", canary)
		}
	}
}

func TestRead_MalformedDoesNotEchoContent(t *testing.T) {
	for _, in := range []string{
		"<rss><channel><item><title>CANARY_BROKEN</title>",
		"<rss><channel><item><title>CANARY_BROKEN</wrong></item></channel></rss>",
		"<rss><channel><item><title>&CANARY_BROKEN;</title></item></channel></rss>",
		"not xml CANARY_BROKEN",
	} {
		_, err := Read(strings.NewReader(in))
		if !errors.Is(err, ErrMalformed) {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if strings.Contains(err.Error(), "CANARY") || strings.Contains(err.Error(), "wrong") {
			t.Errorf("error must not echo content: %v", err)
		}
	}
}
