package web

import (
	"net/http"
	"strconv"
	"time"
)

// PublicCacheSeconds は、Cookieも利用者ごとの内容も持たない公開ページを、ブラウザや配信網がキャッシュしてよい秒数です。
// 更新（お知らせの公開等）が反映されるまでの最大の遅れにもなるため、短めにしています。
const PublicCacheSeconds = 60

// staticCacheSeconds は /static/（CSS・画像）のキャッシュ秒数です。ファイル名に版を含めないため、長くしません。
const staticCacheSeconds = 600

// PublicCache は、応答をキャッシュしてよい公開ページとして扱います。
// Coreの既定（Cache-Control: no-store）を上書きするため、セッションや利用者ごとの内容を含む応答では呼びません。
func PublicCache(w http.ResponseWriter, seconds int) {
	w.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(seconds))
}

// SitemapEntry は sitemap.xml に載せるページです。
type SitemapEntry struct {
	Path    string    // サイト内のパス（例: "/news/1"）
	LastMod time.Time // 最終更新日時。ゼロ値なら載せない
}
