// Package filestest builds the image fixtures of the file tests in Go, so no
// binary test data is checked in: JPEGs with EXIF (GPS and orientation), XMP,
// IPTC and comments, PNGs with text and EXIF chunks, WebP files with metadata
// chunks, an oversized PNG header and an SVG with a script.
package filestest

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
)

// Strings planted in the metadata of the fixtures. A file that was stripped
// contains none of them.
const (
	SecretXMP     = "SECRET-XMP-LOCATION"
	SecretIPTC    = "SECRET-IPTC-CAPTION"
	SecretComment = "SECRET-JPEG-COMMENT"
	SecretText    = "SECRET-PNG-TEXT"
	SecretWebP    = "SECRET-WEBP-XMP"
)

// Red and Blue are the colours of the two halves of the test pictures.
var (
	Red  = color.RGBA{R: 255, A: 255}
	Blue = color.RGBA{B: 255, A: 255}
)

// TwoTone returns a w x h picture whose left half is red and right half blue.
func TwoTone(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			if x < w/2 {
				img.SetRGBA(x, y, Red)
			} else {
				img.SetRGBA(x, y, Blue)
			}
		}
	}
	return img
}

// PlainJPEG is a JPEG without any metadata.
func PlainJPEG(w, h int) []byte {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, TwoTone(w, h), &jpeg.Options{Quality: 90}); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// JPEGWithMeta is a w x h TwoTone JPEG carrying an EXIF segment with the given
// orientation (0 omits the tag) and GPS coordinates, an XMP segment, an IPTC
// (APP13) segment and a comment. It also has an ICC profile segment (APP2),
// which a stripper must keep.
func JPEGWithMeta(w, h, orientation int) []byte {
	base := PlainJPEG(w, h)
	var segs bytes.Buffer
	segment := func(marker byte, payload []byte) {
		segs.Write([]byte{0xFF, marker})
		_ = binary.Write(&segs, binary.BigEndian, uint16(len(payload)+2))
		segs.Write(payload)
	}
	segment(0xE1, append([]byte("Exif\x00\x00"), EXIF(orientation)...))
	segment(0xE1, append([]byte("http://ns.adobe.com/xap/1.0/\x00"), []byte("<x:xmpmeta>"+SecretXMP+"</x:xmpmeta>")...))
	segment(0xED, append([]byte("Photoshop 3.0\x008BIM"), []byte(SecretIPTC)...))
	segment(0xFE, []byte(SecretComment))
	segment(0xE2, append([]byte("ICC_PROFILE\x00\x01\x01"), []byte("fake-profile")...))
	out := append([]byte{}, base[:2]...)
	out = append(out, segs.Bytes()...)
	return append(out, base[2:]...)
}

// EXIF returns a little-endian TIFF structure whose first IFD has the
// orientation tag (omitted when orientation is 0) and a pointer to a GPS IFD
// with a latitude and a longitude.
func EXIF(orientation int) []byte {
	le := binary.LittleEndian
	type entry struct {
		tag, typ uint16
		count    uint32
		value    uint32
	}
	var ifd0 []entry
	if orientation > 0 {
		ifd0 = append(ifd0, entry{0x0112, 3, 1, uint32(orientation)})
	}
	gpsOffset := uint32(8 + 2 + 12*(len(ifd0)+1) + 4)
	ifd0 = append(ifd0, entry{0x8825, 4, 1, gpsOffset})
	dataOffset := gpsOffset + 2 + 12*4 + 4
	gps := []entry{
		{0x0001, 2, 2, uint32('N')},
		{0x0002, 5, 3, dataOffset},
		{0x0003, 2, 2, uint32('E')},
		{0x0004, 5, 3, dataOffset + 24},
	}
	var b bytes.Buffer
	b.WriteString("II")
	_ = binary.Write(&b, le, uint16(42))
	_ = binary.Write(&b, le, uint32(8))
	writeIFD := func(entries []entry) {
		_ = binary.Write(&b, le, uint16(len(entries)))
		for _, e := range entries {
			_ = binary.Write(&b, le, e.tag)
			_ = binary.Write(&b, le, e.typ)
			_ = binary.Write(&b, le, e.count)
			_ = binary.Write(&b, le, e.value)
		}
		_ = binary.Write(&b, le, uint32(0))
	}
	writeIFD(ifd0)
	writeIFD(gps)
	for _, deg := range [][3]uint32{{48, 51, 30}, {2, 17, 40}} {
		for _, v := range deg {
			_ = binary.Write(&b, le, v)
			_ = binary.Write(&b, le, uint32(1))
		}
	}
	return b.Bytes()
}

// PlainPNG is a PNG without metadata chunks.
func PlainPNG(w, h int) []byte {
	var buf bytes.Buffer
	if err := png.Encode(&buf, TwoTone(w, h)); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// AlphaPNG is a PNG with a translucent half.
func AlphaPNG(w, h int) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetNRGBA(x, y, color.NRGBA{R: 200, G: 30, B: 30, A: uint8(255 * x / w)})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// pngChunk renders one PNG chunk with its CRC.
func pngChunk(typ string, data []byte) []byte {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.BigEndian, uint32(len(data)))
	b.WriteString(typ)
	b.Write(data)
	crc := crc32.NewIEEE()
	crc.Write([]byte(typ))
	crc.Write(data)
	_ = binary.Write(&b, binary.BigEndian, crc.Sum32())
	return b.Bytes()
}

// PNGWithMeta is a w x h TwoTone PNG with tEXt, iTXt, zTXt, tIME and eXIf
// chunks (the EXIF one with GPS) inserted after the header.
func PNGWithMeta(w, h int) []byte {
	base := PlainPNG(w, h)
	const headerEnd = 8 + 12 + 13 // signature + IHDR chunk
	var meta bytes.Buffer
	meta.Write(pngChunk("tEXt", []byte("Comment\x00"+SecretText)))
	meta.Write(pngChunk("iTXt", []byte("XML:com.adobe.xmp\x00\x00\x00\x00\x00"+SecretText)))
	meta.Write(pngChunk("zTXt", append([]byte("Software\x00\x00"), []byte(SecretText)...)))
	meta.Write(pngChunk("tIME", []byte{0x07, 0xEA, 3, 1, 12, 0, 0}))
	meta.Write(pngChunk("eXIf", EXIF(1)))
	out := append([]byte{}, base[:headerEnd]...)
	out = append(out, meta.Bytes()...)
	return append(out, base[headerEnd:]...)
}

// HugePNGHeader is a PNG that announces w x h pixels but carries no image
// data: enough for image.DecodeConfig, far too big to decode.
func HugePNGHeader(w, h int) []byte {
	var ihdr bytes.Buffer
	_ = binary.Write(&ihdr, binary.BigEndian, uint32(w))
	_ = binary.Write(&ihdr, binary.BigEndian, uint32(h))
	ihdr.Write([]byte{8, 2, 0, 0, 0})
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	b.Write(pngChunk("IHDR", ihdr.Bytes()))
	b.Write(pngChunk("tEXt", []byte("Comment\x00"+SecretText)))
	b.Write(pngChunk("IEND", nil))
	return b.Bytes()
}

// GIF is a single-frame GIF.
func GIF(w, h int) []byte {
	pal := color.Palette{color.White, color.Black, Red, Blue}
	img := image.NewPaletted(image.Rect(0, 0, w, h), pal)
	for y := range h {
		for x := range w {
			img.SetColorIndex(x, y, uint8(2+x*2/w))
		}
	}
	var buf bytes.Buffer
	if err := gif.Encode(&buf, img, nil); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// HugeGIFHeader is a GIF89a that announces a w x h logical screen but carries
// no image data: enough for image.DecodeConfig, far too big to decode.
func HugeGIFHeader(w, h int) []byte {
	var b bytes.Buffer
	b.WriteString("GIF89a")
	_ = binary.Write(&b, binary.LittleEndian, uint16(w))
	_ = binary.Write(&b, binary.LittleEndian, uint16(h))
	// No global colour table, background colour 0, pixel aspect 0, trailer.
	b.Write([]byte{0, 0, 0, ';'})
	return b.Bytes()
}

func riffChunk(fourcc string, data []byte) []byte {
	var b bytes.Buffer
	b.WriteString(fourcc)
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(data)))
	b.Write(data)
	if len(data)%2 == 1 {
		b.WriteByte(0)
	}
	return b.Bytes()
}

// WebPWithMeta is an extended WebP of w x h pixels with EXIF and XMP chunks
// and a VP8X header announcing them. Its VP8L chunk has a valid header but
// no pixel data: it passes image.DecodeConfig, not a full decode.
func WebPWithMeta(w, h int) []byte {
	vp8x := make([]byte, 10)
	vp8x[0] = 0x08 | 0x04 // EXIF and XMP present
	put24 := func(p []byte, v int) { p[0], p[1], p[2] = byte(v), byte(v>>8), byte(v>>16) }
	put24(vp8x[4:], w-1)
	put24(vp8x[7:], h-1)
	vp8l := []byte{0x2F, 0, 0, 0, 0, 0, 0, 0}
	binary.LittleEndian.PutUint32(vp8l[1:5], uint32(w-1)|uint32(h-1)<<14)
	var body bytes.Buffer
	body.WriteString("WEBP")
	body.Write(riffChunk("VP8X", vp8x))
	body.Write(riffChunk("VP8L", vp8l))
	body.Write(riffChunk("EXIF", append([]byte("Exif\x00\x00"), EXIF(1)...)))
	body.Write(riffChunk("XMP ", []byte("<x:xmpmeta>"+SecretWebP+"</x:xmpmeta>")))
	var out bytes.Buffer
	out.WriteString("RIFF")
	_ = binary.Write(&out, binary.LittleEndian, uint32(body.Len()))
	out.Write(body.Bytes())
	return out.Bytes()
}

// SVGWithScript is an SVG image that runs a script when opened.
func SVGWithScript() []byte {
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"><script>alert(document.cookie)</script><rect width="10" height="10"/></svg>`)
}

// AnimatedWebPWithMeta is an animated WebP (VP8X with the animation flag, an
// ANIM chunk and one ANMF frame) of w x h pixels with an EXIF chunk. The
// standard WebP decoder cannot read animations, so only the VP8X header
// tells the canvas size.
func AnimatedWebPWithMeta(w, h int) []byte {
	vp8x := make([]byte, 10)
	vp8x[0] = 0x02 | 0x08 // animation and EXIF
	put24 := func(p []byte, v int) { p[0], p[1], p[2] = byte(v), byte(v>>8), byte(v>>16) }
	put24(vp8x[4:], w-1)
	put24(vp8x[7:], h-1)
	anim := []byte{0, 0, 0, 0, 0, 0} // background colour, loop count
	anmf := make([]byte, 16+8)       // frame header and a dummy frame payload
	put24(anmf[6:], w-1)
	put24(anmf[9:], h-1)
	var body bytes.Buffer
	body.WriteString("WEBP")
	body.Write(riffChunk("VP8X", vp8x))
	body.Write(riffChunk("ANIM", anim))
	body.Write(riffChunk("ANMF", anmf))
	body.Write(riffChunk("EXIF", append([]byte("Exif\x00\x00"), EXIF(1)...)))
	var out bytes.Buffer
	out.WriteString("RIFF")
	_ = binary.Write(&out, binary.LittleEndian, uint32(body.Len()))
	out.Write(body.Bytes())
	return out.Bytes()
}
