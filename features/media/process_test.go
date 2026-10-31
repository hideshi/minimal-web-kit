package media

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"minimal-web-kit/internal/store"
)

func gradient(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetRGBA(x, y, color.RGBA{uint8(x * 255 / w), uint8(y * 255 / h), 90, 255})
		}
	}
	return img
}

func jpegBytes(t testing.TB, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func pngBytes(t testing.TB, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// withExif は、JPEGの先頭に向きと文字列を持つEXIFを差し込みます。
func withExif(data []byte, orientation uint16, comment string) []byte {
	tiff := []byte("II*\x00\x08\x00\x00\x00")
	tiff = binary.LittleEndian.AppendUint16(tiff, 1)
	tiff = binary.LittleEndian.AppendUint16(tiff, 0x0112)
	tiff = binary.LittleEndian.AppendUint16(tiff, 3)
	tiff = binary.LittleEndian.AppendUint32(tiff, 1)
	tiff = binary.LittleEndian.AppendUint16(tiff, orientation)
	tiff = binary.LittleEndian.AppendUint16(tiff, 0)
	tiff = binary.LittleEndian.AppendUint32(tiff, 0)
	tiff = append(tiff, comment...)
	body := append([]byte("Exif\x00\x00"), tiff...)
	seg := []byte{0xff, 0xe1}
	seg = binary.BigEndian.AppendUint16(seg, uint16(len(body)+2))
	seg = append(seg, body...)
	out := append([]byte{}, data[:2]...)
	out = append(out, seg...)
	return append(out, data[2:]...)
}

// 縮小は長辺だけを合わせ、縦横の比を保ち、小さい画像を拡大しない。
//
// 不変条件: MEDIA-INV-3
func TestProcess_Sizes(t *testing.T) {
	for _, c := range []struct{ w, h, wantW, wantH int }{
		{3200, 2400, 1600, 1200},
		{2400, 3200, 1200, 1600},
		{1600, 1600, 1600, 1600},
		{800, 600, 800, 600},
		{100, 12000, 13, 1600},
		{12000, 1, 1600, 1},
	} {
		out, err := process(pngBytes(t, gradient(c.w, c.h)))
		if err != nil {
			t.Fatalf("%dx%d: %v", c.w, c.h, err)
		}
		if out.width != c.wantW || out.height != c.wantH {
			t.Errorf("%dx%d: got %dx%d", c.w, c.h, out.width, out.height)
		}
		decoded, _, err := image.DecodeConfig(bytes.NewReader(out.data))
		if err != nil || decoded.Width != c.wantW || decoded.Height != c.wantH {
			t.Errorf("%dx%d: decoded %+v %v", c.w, c.h, decoded, err)
		}
	}
}

// EXIFの向きを画素に適用し、撮影位置等の付加情報は保存結果に残さない。
//
// 不変条件: MEDIA-INV-3
func TestProcess_OrientationAndExif(t *testing.T) {
	// 左上が赤、右上が緑、左下が青、右下が白の 80×40
	src := image.NewRGBA(image.Rect(0, 0, 80, 40))
	for y := range 40 {
		for x := range 80 {
			c := color.RGBA{255, 0, 0, 255}
			switch {
			case x >= 40 && y < 20:
				c = color.RGBA{0, 255, 0, 255}
			case x < 40 && y >= 20:
				c = color.RGBA{0, 0, 255, 255}
			case x >= 40:
				c = color.RGBA{255, 255, 255, 255}
			}
			src.SetRGBA(x, y, c)
		}
	}
	base := jpegBytes(t, src)
	const canary = "GPS-CANARY-35.6895N"
	// 各向きで、画面の左上・右上・左下・右下に来る色
	const r, g, b, w = "r", "g", "b", "w"
	for orientation, want := range map[uint16][4]string{
		1: {r, g, b, w},
		2: {g, r, w, b},
		3: {w, b, g, r},
		4: {b, w, r, g},
		5: {r, b, g, w},
		6: {b, r, w, g},
		7: {w, g, b, r},
		8: {g, w, r, b},
	} {
		out, err := process(withExif(base, orientation, canary))
		if err != nil {
			t.Fatalf("orientation %d: %v", orientation, err)
		}
		if bytes.Contains(out.data, []byte(canary)) || bytes.Contains(out.data, []byte("Exif")) {
			t.Fatalf("orientation %d: EXIF remains", orientation)
		}
		decoded, err := jpeg.Decode(bytes.NewReader(out.data))
		if err != nil {
			t.Fatal(err)
		}
		bounds := decoded.Bounds()
		name := func(x, y int) string {
			cr, cg, cb, _ := decoded.At(x, y).RGBA()
			switch {
			case cr > 0xc000 && cg > 0xc000 && cb > 0xc000:
				return w
			case cr > 0xc000:
				return r
			case cg > 0xc000:
				return g
			}
			return b
		}
		got := [4]string{name(4, 4), name(bounds.Dx()-5, 4), name(4, bounds.Dy()-5), name(bounds.Dx()-5, bounds.Dy()-5)}
		if got != want {
			t.Errorf("orientation %d: got %v want %v", orientation, got, want)
		}
	}
}

// 向きの読み取りは、書式の外れた値で止まらず、回転なしにする。
func TestJPEGOrientation_Malformed(t *testing.T) {
	base := jpegBytes(t, gradient(8, 8))
	for name, data := range map[string][]byte{
		"範囲外の値":  withExif(base, 9, ""),
		"値が0":    withExif(base, 0, ""),
		"先頭だけ":   {0xff, 0xd8},
		"長さが壊れた": {0xff, 0xd8, 0xff, 0xe1, 0xff, 0xff, 'E', 'x'},
		"長さが短い":  {0xff, 0xd8, 0xff, 0xe1, 0x00, 0x01},
		"EXIFだけ": append([]byte{0xff, 0xd8, 0xff, 0xe1, 0x00, 0x08}, []byte("Exif\x00\x00")...),
	} {
		if got := jpegOrientation(data); got != 1 {
			t.Errorf("%s: %d", name, got)
		}
	}
	if got := exifOrientation([]byte("II*\x00\xff\xff\xff\xff")); got != 1 {
		t.Errorf("offset: %d", got)
	}
}

// 透明は保ち、透明の無い図は小さいほうの形式で保存する。
//
// 不変条件: MEDIA-INV-3
func TestProcess_PNG(t *testing.T) {
	clear := image.NewNRGBA(image.Rect(0, 0, 40, 40))
	for i := range clear.Pix {
		if i%4 == 3 {
			clear.Pix[i] = 128
		}
	}
	out, err := process(pngBytes(t, clear))
	if err != nil || out.format != formatPNG {
		t.Fatalf("transparent: %s %v", out.format, err)
	}
	decoded, err := png.Decode(bytes.NewReader(out.data))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, a := decoded.At(1, 1).RGBA(); a == 0xffff {
		t.Fatal("alpha lost")
	}
	// 透明の無い写真のような画像は、JPEGのほうが小さい
	photo := gradient(600, 400)
	for i := range photo.Pix {
		if i%4 != 3 {
			photo.Pix[i] ^= uint8(i * 31)
		}
	}
	if out, err := process(pngBytes(t, photo)); err != nil || out.format != formatJPEG {
		t.Fatalf("photo: %s %v", out.format, err)
	}
	// 色数の少ない図は、PNGのほうが小さい
	flat := image.NewRGBA(image.Rect(0, 0, 600, 400))
	for i := range flat.Pix {
		flat.Pix[i] = 255
	}
	if out, err := process(pngBytes(t, flat)); err != nil || out.format != formatPNG {
		t.Fatalf("flat: %s %v", out.format, err)
	}
}

// 中身がJPEG・PNGでないものは断り、大きな画像はデコードの前に断る。
//
// 不変条件: MEDIA-INV-3
func TestProcess_Rejects(t *testing.T) {
	var gifData bytes.Buffer
	if err := gif.Encode(&gifData, gradient(4, 4), nil); err != nil {
		t.Fatal(err)
	}
	good := jpegBytes(t, gradient(16, 16))
	for name, data := range map[string][]byte{
		"空":       {},
		"SVG":     []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`),
		"HTML":    []byte("<html>"),
		"GIF":     gifData.Bytes(),
		"壊れたJPEG": good[:40],
		"壊れたPNG":  []byte("\x89PNG\r\n\x1a\nbroken"),
	} {
		if _, err := process(data); !errors.Is(err, errFormat) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// 中身を読まずに断れるよう、ヘッダーだけで大きさが分かるPNGを作る
	header := func(w, h uint32, depth byte) []byte {
		ihdr := binary.BigEndian.AppendUint32(nil, w)
		ihdr = binary.BigEndian.AppendUint32(ihdr, h)
		ihdr = append(ihdr, depth, 6, 0, 0, 0)
		var b bytes.Buffer
		b.WriteString("\x89PNG\r\n\x1a\n")
		_ = binary.Write(&b, binary.BigEndian, uint32(len(ihdr)))
		b.WriteString("IHDR")
		b.Write(ihdr)
		_ = binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(append([]byte("IHDR"), ihdr...)))
		return b.Bytes()
	}
	for name, data := range map[string][]byte{
		"画素数":       header(5001, 5000, 8),
		"辺の長さ":      header(12001, 100, 8),
		"16ビットの画素数": header(4000, 3001, 16),
	} {
		if _, _, err := inspect(data); !errors.Is(err, errPixels) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// プログレッシブのJPEGを見分け、低い画素数の上限を当てる。
func TestProgressiveJPEG(t *testing.T) {
	data := jpegBytes(t, gradient(16, 16))
	if progressiveJPEG(data) {
		t.Fatal("baseline detected as progressive")
	}
	at := bytes.Index(data, []byte{0xff, 0xc0})
	changed := bytes.Clone(data)
	changed[at+1] = 0xc2
	if !progressiveJPEG(changed) {
		t.Fatal("progressive not detected")
	}
}

// 同時に送っても、全体の容量の上限を超えて保存しない。
//
// 不変条件: MEDIA-INV-6
func TestStore_TotalLimitUnderConcurrency(t *testing.T) {
	core, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()
	if _, err := core.DB().Exec(migrationSQL); err != nil {
		t.Fatal(err)
	}
	s := &Store{db: core.DB(), now: time.Now, limit: 1000}
	p := processed{format: formatJPEG, width: 1, height: 1, data: make([]byte, 300)}
	var wg sync.WaitGroup
	results := make([]error, 8)
	for i := range results {
		wg.Go(func() { _, results[i] = s.Add(context.Background(), p, "説明") })
	}
	wg.Wait()
	saved := 0
	for _, err := range results {
		switch {
		case err == nil:
			saved++
		case errors.Is(err, ErrFull), store.Busy(err):
		default:
			t.Fatalf("add: %v", err)
		}
	}
	total, err := s.Total(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if total > 1000 || total != int64(saved*300) {
		t.Fatalf("total %d, saved %d", total, saved)
	}
}

// 使っている画像はDBの外部キーでも消せず、本体は画像と一緒に消える。
//
// 不変条件: MEDIA-INV-5
func TestStore_DeleteInUse(t *testing.T) {
	core, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()
	if _, err := core.DB().Exec(migrationSQL); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s := &Store{db: core.DB(), now: time.Now, limit: 1 << 20}
	it, err := s.Add(ctx, processed{format: formatPNG, width: 1, height: 1, data: []byte{1}}, "説明")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Atomic(ctx, core.DB(), func(c *sql.Conn) error {
		return replaceRefs(ctx, c, "pages", "1", []string{it.File()})
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, it.ID); !errors.Is(err, ErrInUse) {
		t.Fatalf("delete in use: %v", err)
	}
	if _, err := core.DB().Exec("DELETE FROM media_items WHERE id = ?", it.ID); err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Fatalf("foreign key: %v", err)
	}
	if err := store.Atomic(ctx, core.DB(), func(c *sql.Conn) error { return replaceRefs(ctx, c, "pages", "1", nil) }); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, it.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Blob(ctx, it.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("blob remains: %v", err)
	}
}

// FuzzProcess は、どんなファイルでも止まらず、受け付けたら上限内の画像を返すことを確かめる。
func FuzzProcess(f *testing.F) {
	f.Add(jpegBytes(f, gradient(32, 16)))
	f.Add(pngBytes(f, gradient(16, 32)))
	f.Add(withExif(jpegBytes(f, gradient(16, 16)), 6, "x"))
	f.Add([]byte("\x89PNG\r\n\x1a\n"))
	f.Add([]byte{0xff, 0xd8, 0xff, 0xe1, 0x00, 0x10})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<16 {
			return
		}
		_ = jpegOrientation(data)
		// 小さな入力でも画素数の上限まで展開され得るため、大きな画像はヘッダーの検査までにする
		if _, cfg, err := inspect(data); err != nil || cfg.Width*cfg.Height > 1<<20 {
			return
		}
		out, err := process(data)
		if err != nil {
			return
		}
		if out.width > maxLongSide || out.height > maxLongSide || len(out.data) == 0 {
			t.Fatalf("output %dx%d %d bytes", out.width, out.height, len(out.data))
		}
	})
}
