package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
)

const (
	// maxFileBytes は、受け取るファイルの上限です。
	maxFileBytes = 8_000_000
	// maxLongSide は、保存する画像の長辺です。本文の幅の約2倍で、高精細な画面でも足ります。
	maxLongSide = 1600
	jpegQuality = 80
	// maxPixels は、デコードを許す画素数です。小さなファイルが巨大に展開される画像を断ります。
	maxPixels = 24_000_000
	// maxHeavyPixels は、同じ画素数でメモリを多く使う形式（プログレッシブのJPEG、16ビットのPNG等）の上限です。
	maxHeavyPixels = 12_000_000
	maxEdge        = 12_000

	formatJPEG = "jpg"
	formatPNG  = "png"
)

var (
	errFormat = errors.New("media: unsupported format")
	errPixels = errors.New("media: too many pixels")
)

// processed は、保存する画像です。
type processed struct {
	format        string
	width, height int
	data          []byte
}

// process は、受け取ったファイルを検査し、縮小・再エンコードして返します（MEDIA-INV-3）。
// 再エンコードでEXIF全体を落とすため、撮影位置は保存されません。元のファイルは残しません。
func process(data []byte) (processed, error) {
	format, cfg, err := inspect(data)
	if err != nil {
		return processed{}, err
	}
	var src image.Image
	if format == formatJPEG {
		src, err = jpeg.Decode(bytes.NewReader(data))
	} else {
		src, err = png.Decode(bytes.NewReader(data))
	}
	if err != nil {
		return processed{}, errFormat
	}
	b := src.Bounds()
	if b.Dx() != cfg.Width || b.Dy() != cfg.Height {
		return processed{}, errFormat
	}
	orientation := 1
	if format == formatJPEG {
		orientation = jpegOrientation(data)
	}
	out := oriented(scaled(src), orientation)
	return encode(out, format)
}

// inspect は、中身から形式を判定し、デコードの前に大きさを確かめます。拡張子と申告のContent-Typeは見ません。
func inspect(data []byte) (string, image.Config, error) {
	var format string
	var cfg image.Config
	var err error
	heavy := false
	switch {
	case bytes.HasPrefix(data, []byte{0xff, 0xd8, 0xff}):
		format = formatJPEG
		cfg, err = jpeg.DecodeConfig(bytes.NewReader(data))
		if err == nil {
			// 形式が分からないものを通常のJPEGとして扱わない
			progressive, ok := jpegFrame(data)
			if !ok {
				return "", cfg, errFormat
			}
			heavy = progressive || cfg.ColorModel == color.CMYKModel
		}
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		format = formatPNG
		cfg, err = png.DecodeConfig(bytes.NewReader(data))
		heavy = err == nil && (cfg.ColorModel == color.RGBA64Model || cfg.ColorModel == color.NRGBA64Model || cfg.ColorModel == color.Gray16Model)
	default:
		return "", cfg, errFormat
	}
	if err != nil {
		return "", cfg, errFormat
	}
	limit := int64(maxPixels)
	if heavy {
		limit = maxHeavyPixels
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > maxEdge || cfg.Height > maxEdge || int64(cfg.Width)*int64(cfg.Height) > limit {
		return "", cfg, errPixels
	}
	return format, cfg, nil
}

// jpegSegments は、JPEGの部品を順に visit へ渡し、画像の本体（SOS）の手前で止まります。
// 走査の上限は置きません。部品の見出しだけを読み、部品は最大64KBで、入力は上限の8MB以下なので、時間は長さに比例します。
func jpegSegments(data []byte, visit func(marker byte, body []byte) bool) {
	for i := 2; i+4 <= len(data); {
		if data[i] != 0xff {
			return
		}
		marker := data[i+1]
		if marker == 0xff { // 埋め草
			i++
			continue
		}
		if marker == 0xda {
			return
		}
		if marker == 0x01 || (marker >= 0xd0 && marker <= 0xd8) { // 長さを持たない印
			i += 2
			continue
		}
		length := int(binary.BigEndian.Uint16(data[i+2:]))
		if length < 2 || i+2+length > len(data) {
			return
		}
		if !visit(marker, data[i+4:i+2+length]) {
			return
		}
		i += 2 + length
	}
}

// jpegFrame は、最初のフレームの見出し（SOF）を探して、プログレッシブかを返します。
// 見つからなければ ok=false です（部品の間に余分なバイトがあるファイル等）。
func jpegFrame(data []byte) (progressive, ok bool) {
	jpegSegments(data, func(marker byte, _ []byte) bool {
		if marker < 0xc0 || marker > 0xcf || marker == 0xc4 || marker == 0xc8 || marker == 0xcc {
			return true
		}
		progressive, ok = marker == 0xc2, true
		return false
	})
	return progressive, ok
}

// jpegOrientation は、EXIFの向き（1〜8）を返します。読むのは向きだけで、書式の外れた値は1（回転なし）にします。
func jpegOrientation(data []byte) int {
	result := 1
	jpegSegments(data, func(marker byte, body []byte) bool {
		if marker != 0xe1 || !bytes.HasPrefix(body, []byte("Exif\x00\x00")) {
			return true
		}
		result = exifOrientation(body[6:])
		return false
	})
	return result
}

func exifOrientation(tiff []byte) int {
	if len(tiff) < 8 {
		return 1
	}
	var order binary.ByteOrder
	switch string(tiff[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 1
	}
	offset := int64(order.Uint32(tiff[4:]))
	if offset+2 > int64(len(tiff)) {
		return 1
	}
	count := int64(order.Uint16(tiff[offset:]))
	for i := int64(0); i < count; i++ {
		at := offset + 2 + i*12
		if at+12 > int64(len(tiff)) {
			return 1
		}
		if order.Uint16(tiff[at:]) == 0x0112 {
			if v := int(order.Uint16(tiff[at+8:])); v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}

// target は、長辺が limit に収まる大きさを返します。拡大はしません。
func target(w, h, limit int) (int, int) {
	if w <= limit && h <= limit {
		return w, h
	}
	if w >= h {
		return limit, max(1, int(math.Round(float64(h)*float64(limit)/float64(w))))
	}
	return max(1, int(math.Round(float64(w)*float64(limit)/float64(h)))), limit
}

// rowReader は、画像の1行を、乗算済みのRGBA（1画素4バイト）で dst に書く関数を返します。
func rowReader(src image.Image) func(y int, dst []uint8) {
	b := src.Bounds()
	switch s := src.(type) {
	case *image.YCbCr:
		return func(y int, dst []uint8) {
			for x := 0; x < b.Dx(); x++ {
				yi := s.YOffset(b.Min.X+x, b.Min.Y+y)
				ci := s.COffset(b.Min.X+x, b.Min.Y+y)
				dst[x*4], dst[x*4+1], dst[x*4+2] = color.YCbCrToRGB(s.Y[yi], s.Cb[ci], s.Cr[ci])
				dst[x*4+3] = 255
			}
		}
	case *image.RGBA:
		return func(y int, dst []uint8) {
			copy(dst, s.Pix[s.PixOffset(b.Min.X, b.Min.Y+y):][:b.Dx()*4])
		}
	}
	return func(y int, dst []uint8) {
		for x := 0; x < b.Dx(); x++ {
			r, g, bl, a := src.At(b.Min.X+x, b.Min.Y+y).RGBA()
			dst[x*4], dst[x*4+1], dst[x*4+2], dst[x*4+3] = uint8(r>>8), uint8(g>>8), uint8(bl>>8), uint8(a>>8)
		}
	}
}

// weight は、出力の1画素が使う入力の範囲と重みです。
type weight struct {
	start int
	w     []float32
}

func lanczos(x float64) float64 {
	if x == 0 {
		return 1
	}
	if math.Abs(x) >= 3 {
		return 0
	}
	p := math.Pi * x
	return 3 * math.Sin(p) * math.Sin(p/3) / (p * p)
}

func weights(srcSize, dstSize int) []weight {
	ratio := float64(srcSize) / float64(dstSize)
	scale := math.Max(ratio, 1)
	out := make([]weight, dstSize)
	for i := range out {
		center := (float64(i)+0.5)*ratio - 0.5
		first := max(0, int(math.Ceil(center-3*scale)))
		last := min(srcSize-1, int(math.Floor(center+3*scale)))
		ws := make([]float32, last-first+1)
		var sum float64
		for j := first; j <= last; j++ {
			v := lanczos((float64(j) - center) / scale)
			ws[j-first] = float32(v)
			sum += v
		}
		for k := range ws {
			ws[k] /= float32(sum)
		}
		out[i] = weight{first, ws}
	}
	return out
}

// scaled は、画像を長辺 maxLongSide まで縮めます。元の画像を複製しないよう、1行ずつ読み、
// 書きかけの出力の行だけをメモリに持ちます（処理のメモリを、元の画像と出力の分に抑える）。
func scaled(src image.Image) *image.RGBA {
	sw, sh := src.Bounds().Dx(), src.Bounds().Dy()
	dw, dh := target(sw, sh, maxLongSide)
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	read := rowReader(src)
	row := make([]uint8, sw*4)
	if dw == sw && dh == sh {
		for y := 0; y < sh; y++ {
			read(y, dst.Pix[y*dst.Stride:][:sw*4])
		}
		return dst
	}
	wx, wy := weights(sw, dw), weights(sh, dh)
	line := make([]float32, dw*4)
	pending := map[int][]float32{}
	first := 0 // まだ書き終えていない最初の出力の行
	for y := 0; y < sh; y++ {
		read(y, row)
		for x := range dw {
			var c0, c1, c2, c3 float32
			at := wx[x].start * 4
			for _, w := range wx[x].w {
				c0 += w * float32(row[at])
				c1 += w * float32(row[at+1])
				c2 += w * float32(row[at+2])
				c3 += w * float32(row[at+3])
				at += 4
			}
			line[x*4], line[x*4+1], line[x*4+2], line[x*4+3] = c0, c1, c2, c3
		}
		for dy := first; dy < dh && wy[dy].start <= y; dy++ {
			if y >= wy[dy].start+len(wy[dy].w) {
				continue
			}
			acc := pending[dy]
			if acc == nil {
				acc = make([]float32, dw*4)
				pending[dy] = acc
			}
			w := wy[dy].w[y-wy[dy].start]
			for i, v := range line {
				acc[i] += w * v
			}
			if y == wy[dy].start+len(wy[dy].w)-1 {
				writeRow(dst, dy, acc)
				delete(pending, dy)
			}
		}
		for first < dh && pending[first] == nil && wy[first].start+len(wy[first].w)-1 <= y {
			first++
		}
	}
	return dst
}

func writeRow(dst *image.RGBA, y int, acc []float32) {
	out := dst.Pix[y*dst.Stride:]
	for i := 0; i < len(acc); i += 4 {
		a := clamp(acc[i+3])
		out[i], out[i+1], out[i+2], out[i+3] = min(clamp(acc[i]), a), min(clamp(acc[i+1]), a), min(clamp(acc[i+2]), a), a
	}
}

func clamp(v float32) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return uint8(v + 0.5)
}

// oriented は、EXIFの向きに従って画素を回転・反転します。
func oriented(src *image.RGBA, orientation int) *image.RGBA {
	if orientation < 2 || orientation > 8 {
		return src
	}
	w, h := src.Rect.Dx(), src.Rect.Dy()
	dw, dh := w, h
	if orientation >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := range h {
		for x := range w {
			var nx, ny int
			switch orientation {
			case 2:
				nx, ny = w-1-x, y
			case 3:
				nx, ny = w-1-x, h-1-y
			case 4:
				nx, ny = x, h-1-y
			case 5:
				nx, ny = y, x
			case 6:
				nx, ny = h-1-y, x
			case 7:
				nx, ny = h-1-y, w-1-x
			case 8:
				nx, ny = y, w-1-x
			}
			copy(dst.Pix[ny*dst.Stride+nx*4:][:4], src.Pix[y*src.Stride+x*4:][:4])
		}
	}
	return dst
}

func opaque(img *image.RGBA) bool {
	for i := 3; i < len(img.Pix); i += 4 {
		if img.Pix[i] != 255 {
			return false
		}
	}
	return true
}

// encode は、写真はJPEGに、図等はPNGにします。透明の無いPNGは、JPEGも作って小さいほうを保存します。
func encode(img *image.RGBA, format string) (processed, error) {
	result := processed{width: img.Rect.Dx(), height: img.Rect.Dy()}
	var jpg bytes.Buffer
	if format == formatJPEG || opaque(img) {
		if err := jpeg.Encode(&jpg, img, &jpeg.Options{Quality: jpegQuality}); err != nil {
			return result, err
		}
	}
	if format == formatJPEG {
		result.format, result.data = formatJPEG, jpg.Bytes()
		return result, nil
	}
	var pngBuf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&pngBuf, img); err != nil {
		return result, err
	}
	if jpg.Len() > 0 && jpg.Len() < pngBuf.Len() {
		result.format, result.data = formatJPEG, jpg.Bytes()
		return result, nil
	}
	result.format, result.data = formatPNG, pngBuf.Bytes()
	return result, nil
}
