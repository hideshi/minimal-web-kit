package pages

import (
	"context"
	"errors"
	"testing"
)

// TestImport_SameChecksAsAdmin は、道具からの作成が、管理画面の保存と同じ検査を通り、下書きになることを確かめる。
// 不変条件: PAGES-INV-3
func TestImport_SameChecksAsAdmin(t *testing.T) {
	fx := setup(t)
	ctx := context.Background()
	fx.create("about", "会社概要", "", "", false)

	id, err := fx.f.Import(ctx, Input{Slug: "company", Title: "会社", Body: "## 見出し\n\n本文"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := fx.f.store.Get(ctx, id)
	if err != nil || p.Published || p.Slug != "company" {
		t.Fatalf("imported page must be a draft: %+v %v", p, err)
	}
	for _, c := range []struct {
		in    Input
		field string
	}{
		{Input{Slug: "about", Title: "題"}, "slug_taken"},
		{Input{Slug: "news", Title: "題"}, "slug_reserved"},
		{Input{Slug: "A", Title: "題"}, "slug"},
		{Input{Slug: "ok-slug", Title: ""}, "title"},
	} {
		var invalid InputError
		if _, err := fx.f.Import(ctx, c.in); !errors.As(err, &invalid) || invalid.Field != c.field {
			t.Errorf("Import(%q) = %v, want %s", c.in.Slug, err, c.field)
		}
	}

	for slug, want := range map[string]error{
		"new-page": nil, "news": InputError{"slug_reserved"}, "privacy": InputError{"slug_reserved"}, "a_b": InputError{"slug"},
	} {
		if err := fx.f.CheckSlug(slug); err != want {
			t.Errorf("CheckSlug(%q) = %v, want %v", slug, err, want)
		}
	}
	slugs, err := fx.f.Slugs(ctx)
	if err != nil || !slugs["about"] || !slugs["company"] || len(slugs) != 2 {
		t.Fatalf("slugs: %v %v", slugs, err)
	}
}
