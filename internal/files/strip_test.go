package files

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/donaldsteele/bubbles/internal/files/filestest"
)

// jpegSegments lists the marker codes of the segments before the first scan.
func jpegSegments(t *testing.T, data []byte) []byte {
	t.Helper()
	var codes []byte
	for pos := 2; pos+4 <= len(data); {
		if data[pos] != 0xFF {
			t.Fatalf("no marker at %d", pos)
		}
		code := data[pos+1]
		codes = append(codes, code)
		if code == 0xDA {
			return codes
		}
		pos += 2 + int(binary.BigEndian.Uint16(data[pos+2:]))
	}
	return codes
}

func pixelsEqual(a, b image.Image) bool {
	if a.Bounds() != b.Bounds() {
		return false
	}
	for y := a.Bounds().Min.Y; y < a.Bounds().Max.Y; y++ {
		for x := a.Bounds().Min.X; x < a.Bounds().Max.X; x++ {
			ar, ag, ab, aa := a.At(x, y).RGBA()
			br, bg, bb, ba := b.At(x, y).RGBA()
			if ar != br || ag != bg || ab != bb || aa != ba {
				return false
			}
		}
	}
	return true
}

func TestFilesStripJPEGRemovesMetadataLosslessly(t *testing.T) {
	src := filestest.JPEGWithMeta(48, 32, 1)
	var out bytes.Buffer
	if err := StripJPEG(&out, bytes.NewReader(src)); err != nil {
		t.Fatal(err)
	}
	noSecrets(t, "stripped JPEG", out.Bytes(), filestest.SecretXMP, filestest.SecretIPTC, filestest.SecretComment, "Exif")
	for _, code := range jpegSegments(t, out.Bytes()) {
		if code == 0xE1 || code == 0xED || code == 0xFE {
			t.Errorf("segment %#x survived", code)
		}
	}
	if !bytes.Contains(out.Bytes(), []byte("ICC_PROFILE")) {
		t.Error("the colour profile segment was dropped")
	}
	want, err := jpeg.Decode(bytes.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	got, err := jpeg.Decode(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("stripped JPEG does not decode: %v", err)
	}
	if !pixelsEqual(want, got) {
		t.Error("pixels changed: the strip is not lossless")
	}
	var again bytes.Buffer
	if err := StripJPEG(&again, bytes.NewReader(out.Bytes())); err != nil || !bytes.Equal(again.Bytes(), out.Bytes()) {
		t.Errorf("stripping is not idempotent: %v", err)
	}
}

func TestFilesStripJPEGDropsDataAfterEOI(t *testing.T) {
	src := append(filestest.PlainJPEG(16, 16), []byte("TRAILING-MOTION-PHOTO-DATA")...)
	var out bytes.Buffer
	if err := StripJPEG(&out, bytes.NewReader(src)); err != nil {
		t.Fatal(err)
	}
	noSecrets(t, "stripped JPEG", out.Bytes(), "TRAILING")
}

func TestFilesStripJPEGRejectsMalformed(t *testing.T) {
	good := filestest.JPEGWithMeta(32, 32, 1)
	tests := map[string][]byte{
		"empty":            nil,
		"not a jpeg":       []byte("GIF89a......"),
		"truncated header": good[:10],
		"truncated scan":   good[:len(good)-40],
		"bad length":       append(append([]byte{0xFF, 0xD8}, 0xFF, 0xE0, 0x00, 0x01), good[2:]...),
	}
	for name, in := range tests {
		err := StripJPEG(&bytes.Buffer{}, bytes.NewReader(in))
		if !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: err = %v, want ErrMalformed", name, err)
		}
	}
}

func TestFilesStripPNGRemovesMetadataChunks(t *testing.T) {
	src := filestest.PNGWithMeta(24, 16)
	var out bytes.Buffer
	if err := StripPNG(&out, bytes.NewReader(src)); err != nil {
		t.Fatal(err)
	}
	for _, typ := range []string{"tEXt", "iTXt", "zTXt", "tIME", "eXIf"} {
		if !bytes.Contains(src, []byte(typ)) {
			t.Fatalf("fixture has no %s chunk", typ)
		}
		if bytes.Contains(out.Bytes(), []byte(typ)) {
			t.Errorf("chunk %s survived", typ)
		}
	}
	noSecrets(t, "stripped PNG", out.Bytes(), filestest.SecretText)
	want, err := png.Decode(bytes.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	got, err := png.Decode(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("stripped PNG does not decode: %v", err)
	}
	if !pixelsEqual(want, got) {
		t.Error("pixels changed")
	}
	if !bytes.Equal(out.Bytes(), filestest.PlainPNG(24, 16)) {
		t.Error("stripping the fixture should give back the plain PNG")
	}
}

func TestFilesStripPNGRejectsMalformed(t *testing.T) {
	good := filestest.PNGWithMeta(8, 8)
	tests := map[string][]byte{
		"empty":     nil,
		"signature": []byte("\x89PNG\r\n\x1a"),
		"truncated": good[:len(good)-20],
		"no IEND":   good[:len(good)-12],
		"bad type":  append(append([]byte{}, good[:8]...), 0, 0, 0, 0, '1', '2', '3', '4', 0, 0, 0, 0),
	}
	for name, in := range tests {
		if err := StripPNG(&bytes.Buffer{}, bytes.NewReader(in)); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: err = %v, want ErrMalformed", name, err)
		}
	}
}

// riffChunks lists the chunk ids of a WebP file and checks the RIFF size.
func riffChunks(t *testing.T, data []byte) []string {
	t.Helper()
	if string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		t.Fatal("not a WebP file")
	}
	if got := int(binary.LittleEndian.Uint32(data[4:8])); got != len(data)-8 {
		t.Errorf("RIFF size = %d, want %d", got, len(data)-8)
	}
	var ids []string
	for pos := 12; pos < len(data); {
		size := int(binary.LittleEndian.Uint32(data[pos+4:]))
		ids = append(ids, string(data[pos:pos+4]))
		pos += 8 + size + size&1
	}
	return ids
}

func TestFilesStripWebPRemovesMetadataAndClearsFlags(t *testing.T) {
	src := filestest.WebPWithMeta(5, 3)
	var out bytes.Buffer
	if err := StripWebP(&out, bytes.NewReader(src), int64(len(src))); err != nil {
		t.Fatal(err)
	}
	ids := riffChunks(t, out.Bytes())
	if len(ids) != 2 || ids[0] != "VP8X" || ids[1] != "VP8L" {
		t.Errorf("chunks = %v, want [VP8X VP8L]", ids)
	}
	noSecrets(t, "stripped WebP", out.Bytes(), filestest.SecretWebP, "Exif")
	if flags := out.Bytes()[12+8]; flags&0x0C != 0 {
		t.Errorf("VP8X flags = %#x: EXIF or XMP flag still set", flags)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(out.Bytes()))
	if err != nil || cfg.Width != 5 || cfg.Height != 3 {
		t.Errorf("stripped WebP config = %+v, %v", cfg, err)
	}
	var again bytes.Buffer
	if err := StripWebP(&again, bytes.NewReader(out.Bytes()), int64(out.Len())); err != nil || !bytes.Equal(again.Bytes(), out.Bytes()) {
		t.Errorf("stripping is not idempotent: %v", err)
	}
}

func TestFilesStripWebPKeepsOddChunkPadding(t *testing.T) {
	// A RIFF with one odd-sized chunk (3 bytes + pad) and an EXIF chunk.
	var body bytes.Buffer
	body.WriteString("WEBP")
	for _, c := range []struct {
		id   string
		data string
	}{{"ABCD", "xyz"}, {"EXIF", "secret"}} {
		body.WriteString(c.id)
		_ = binary.Write(&body, binary.LittleEndian, uint32(len(c.data)))
		body.WriteString(c.data)
		if len(c.data)%2 == 1 {
			body.WriteByte(0)
		}
	}
	var src bytes.Buffer
	src.WriteString("RIFF")
	_ = binary.Write(&src, binary.LittleEndian, uint32(body.Len()))
	src.Write(body.Bytes())
	var out bytes.Buffer
	if err := StripWebP(&out, bytes.NewReader(src.Bytes()), int64(src.Len())); err != nil {
		t.Fatal(err)
	}
	if ids := riffChunks(t, out.Bytes()); len(ids) != 1 || ids[0] != "ABCD" || out.Len() != 12+8+4 {
		t.Errorf("chunks %v, %d bytes", ids, out.Len())
	}
}

func TestFilesStripWebPRejectsMalformed(t *testing.T) {
	good := filestest.WebPWithMeta(4, 4)
	tests := map[string][]byte{
		"empty":      nil,
		"not riff":   []byte("RIFX\x04\x00\x00\x00WEBP"),
		"truncated":  good[:len(good)-5],
		"huge chunk": append(append([]byte{}, good[:16]...), 0xFF, 0xFF, 0xFF, 0x7F),
	}
	for name, in := range tests {
		err := StripWebP(&bytes.Buffer{}, bytes.NewReader(in), int64(len(in)))
		if !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: err = %v, want ErrMalformed", name, err)
		}
	}
}

func TestFilesTIFFOrientation(t *testing.T) {
	for o := 0; o <= 8; o++ {
		want := max(o, 1)
		if got := tiffOrientation(filestest.EXIF(o)); got != want {
			t.Errorf("orientation %d read as %d, want %d", o, got, want)
		}
	}
	// Big-endian byte order.
	be := []byte{'M', 'M', 0, 42, 0, 0, 0, 8, 0, 1, 0x01, 0x12, 0, 3, 0, 0, 0, 1, 0, 8, 0, 0, 0, 0, 0, 0}
	if got := tiffOrientation(be); got != 8 {
		t.Errorf("big-endian orientation = %d, want 8", got)
	}
	for name, in := range map[string][]byte{
		"empty": nil, "short": {'I', 'I', 42, 0}, "bad order": {'X', 'X', 0, 42, 0, 0, 0, 8},
		"offset past end": {'I', 'I', 42, 0, 0xFF, 0xFF, 0, 0}, "bad magic": {'I', 'I', 41, 0, 8, 0, 0, 0, 0, 0},
	} {
		if got := tiffOrientation(in); got != 1 {
			t.Errorf("%s: orientation = %d, want 1", name, got)
		}
	}
	// Out-of-range value.
	bad := filestest.EXIF(6)
	binary.LittleEndian.PutUint16(bad[8+2+8:], 9)
	if got := tiffOrientation(bad); got != 1 {
		t.Errorf("orientation 9 read as %d, want 1", got)
	}
}

func TestFilesJPEGMetaReadsOrientationAndProfile(t *testing.T) {
	o, icc := jpegMeta(bytes.NewReader(filestest.JPEGWithMeta(16, 16, 6)))
	if o != 6 {
		t.Errorf("orientation = %d, want 6", o)
	}
	if !bytes.Contains(icc, []byte("ICC_PROFILE")) || icc[1] != 0xE2 {
		t.Errorf("ICC segments = %q", icc)
	}
	if o, icc := jpegMeta(bytes.NewReader(filestest.PlainJPEG(8, 8))); o != 1 || icc != nil {
		t.Errorf("plain JPEG: orientation %d, icc %v", o, icc)
	}
	if o, _ := jpegMeta(bytes.NewReader([]byte("garbage"))); o != 1 {
		t.Errorf("garbage: orientation %d", o)
	}
}

func TestFilesOrientMapsEveryOrientation(t *testing.T) {
	// Source 3 wide, 2 tall:  a b c / d e f   (ids 0..5)
	src := image.NewRGBA(image.Rect(0, 0, 3, 2))
	for i := range 6 {
		src.SetRGBA(i%3, i/3, color.RGBA{R: uint8(i), A: 255})
	}
	// Expected result rows as source ids.
	tests := []struct {
		o    int
		w, h int
		rows [][]int
	}{
		{2, 3, 2, [][]int{{2, 1, 0}, {5, 4, 3}}},
		{3, 3, 2, [][]int{{5, 4, 3}, {2, 1, 0}}},
		{4, 3, 2, [][]int{{3, 4, 5}, {0, 1, 2}}},
		{5, 2, 3, [][]int{{0, 3}, {1, 4}, {2, 5}}},
		{6, 2, 3, [][]int{{3, 0}, {4, 1}, {5, 2}}},
		{7, 2, 3, [][]int{{5, 2}, {4, 1}, {3, 0}}},
		{8, 2, 3, [][]int{{2, 5}, {1, 4}, {0, 3}}},
	}
	for _, tc := range tests {
		got := orient(src, tc.o)
		if got.Bounds().Dx() != tc.w || got.Bounds().Dy() != tc.h {
			t.Errorf("orientation %d: size %v, want %dx%d", tc.o, got.Bounds().Size(), tc.w, tc.h)
			continue
		}
		for y, row := range tc.rows {
			for x, id := range row {
				if r := got.RGBAAt(x, y).R; int(r) != id {
					t.Errorf("orientation %d: pixel (%d,%d) = %d, want %d", tc.o, x, y, r, id)
				}
			}
		}
	}
}
