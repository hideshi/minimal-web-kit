package media

import (
	"context"
	"errors"

	"minimal-web-kit/internal/web/form"
	"minimal-web-kit/internal/web/richtext"
)

// 道具（移行等）から画像を追加するときの失敗の種類です。どれも、その画像を取り込まなかったことを示します。
var (
	// ErrImportAlt は、説明が1行の1〜200文字でないことを示します。
	ErrImportAlt = errors.New("media: invalid alt")
	// ErrImportSize は、ファイルが受け取る上限（8,000,000バイト）を超えることを示します。
	ErrImportSize = errors.New("media: file too large")
	// ErrImportFormat は、中身がJPEG・PNGでないか、読めないことを示します。
	ErrImportFormat = errors.New("media: unsupported format")
	// ErrImportPixels は、画素数が上限を超えることを示します。
	ErrImportPixels = errors.New("media: too many pixels")
	// ErrImportBusy は、加工の枠が空かなかったことを示します。
	ErrImportBusy = errors.New("media: busy")
)

// Import は、HTTPの外（移行の道具等）から画像を1枚追加し、本文に書くファイル名（<id>.<ext>）を返します。
// 管理画面の追加と同じ検査（説明・大きさ・形式・画素数）、加工（縮小・再エンコード・EXIFを落とす）、
// 全体の容量の確認（ErrFull）を通します。加工は同じ同時処理の枠を使います。
// 不変条件: MEDIA-INV-3, MEDIA-INV-4, MEDIA-INV-6
func (f *Feature) Import(ctx context.Context, data []byte, alt string) (string, error) {
	clean, err := form.Line(alt, richtext.MaxAltRunes, true)
	if err != nil {
		return "", ErrImportAlt
	}
	if len(data) > maxFileBytes {
		return "", ErrImportSize
	}
	if !f.acquire(ctx) {
		return "", ErrImportBusy
	}
	var result processed
	func() {
		defer func() { <-f.slots }() // 加工の途中のpanicでも枠を返す
		result, err = process(data)
	}()
	switch {
	case errors.Is(err, errPixels):
		return "", ErrImportPixels
	case err != nil:
		return "", ErrImportFormat
	}
	it, err := f.store.Add(ctx, result, clean)
	if err != nil {
		return "", err
	}
	return it.File(), nil
}
