package media

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"minimal-web-kit/internal/store"
	"minimal-web-kit/internal/web/richtext"
)

// TestImport_SameChecksAsUpload は、道具からの追加が、管理画面と同じ検査・加工・容量の上限を通ることを確かめる。
// 不変条件: MEDIA-INV-3, MEDIA-INV-4, MEDIA-INV-6
func TestImport_SameChecksAsUpload(t *testing.T) {
	core, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()
	f := NewFeature(nil)
	f.slotWait = time.Millisecond
	if err := f.Init(core.DB()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	big := pngBytes(t, gradient(2400, 1200))
	file, err := f.Import(ctx, big, "  本社の外観  ")
	if err != nil {
		t.Fatal(err)
	}
	if !richtext.ImageFile(file) {
		t.Fatalf("file name: %q", file)
	}
	it, err := f.store.Get(ctx, strings.TrimSuffix(strings.TrimSuffix(file, ".jpg"), ".png"))
	if err != nil || it.Alt != "本社の外観" || max(it.Width, it.Height) != maxLongSide {
		t.Fatalf("saved: %+v %v", it, err)
	}

	for _, c := range []struct {
		data []byte
		alt  string
		want error
	}{
		{big, "", ErrImportAlt},
		{big, "a\nb", ErrImportAlt},
		{big, strings.Repeat("あ", 201), ErrImportAlt},
		{make([]byte, maxFileBytes+1), "説明", ErrImportSize},
		{[]byte("GIF89a"), "説明", ErrImportFormat},
		{[]byte("<svg></svg>"), "説明", ErrImportFormat},
	} {
		if _, err := f.Import(ctx, c.data, c.alt); !errors.Is(err, c.want) {
			t.Errorf("Import(%d bytes, %q) = %v, want %v", len(c.data), c.alt, err, c.want)
		}
	}

	f.store.limit = it.Bytes + 10
	if _, err := f.Import(ctx, jpegBytes(t, gradient(64, 64)), "説明"); !errors.Is(err, ErrFull) {
		t.Fatalf("total limit: %v", err)
	}
	f.store.limit = 1 << 40

	f.slots <- struct{}{}
	if _, err := f.Import(ctx, jpegBytes(t, gradient(8, 8)), "説明"); !errors.Is(err, ErrImportBusy) {
		t.Fatalf("busy: %v", err)
	}
	<-f.slots

	var n int
	if err := core.DB().QueryRow("SELECT COUNT(*) FROM media_items").Scan(&n); err != nil || n != 1 {
		t.Fatalf("only the first image must be saved: %d %v", n, err)
	}
	// EXIFは再エンコードで落ちる（加工は管理画面と同じ process。EXIFの細かな確かめは TestProcess_OrientationAndExif）
	file, err = f.Import(ctx, withExif(jpegBytes(t, gradient(8, 8)), 1, "GPS"), "説明")
	if err != nil {
		t.Fatal(err)
	}
	blob, _ := f.store.Blob(ctx, strings.TrimSuffix(strings.TrimSuffix(file, ".jpg"), ".png"))
	if bytes.Contains(blob, []byte("Exif")) || bytes.Contains(blob, []byte("GPS")) {
		t.Fatal("EXIF must be dropped")
	}
}
