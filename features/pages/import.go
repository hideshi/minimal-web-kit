package pages

import "context"

// Import は、HTTPの外（移行の道具等）から固定ページを下書きで1件作り、IDを返します。
// 管理画面の保存と同じ検査（スラッグ・予約語・登録済みの経路・本文・画像の参照）を通り、ナビも作り直します（PAGES-INV-6）。
func (f *Feature) Import(ctx context.Context, in Input) (int64, error) {
	return f.save(ctx, 0, in)
}

// CheckSlug は、スラッグが形の規則に合い、予約語・登録済みの経路に当たらないかを返します。
// 既にあるページとの重複は確かめません（Slugs で確かめる）。移行の道具が、取り込む前に全部のスラッグを決めるために使います。
func (f *Feature) CheckSlug(slug string) error {
	return checkSlug(slug, f.taken)
}

// Slugs は、既にあるページのスラッグを返します。
func (f *Feature) Slugs(ctx context.Context) (map[string]bool, error) {
	pages, err := f.store.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(pages))
	for _, p := range pages {
		out[p.Slug] = true
	}
	return out, nil
}
