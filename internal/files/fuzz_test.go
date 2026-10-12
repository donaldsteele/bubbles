package files

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/donaldsteele/bubbles/internal/files/filestest"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

func FuzzStripJPEG(f *testing.F) {
	good := filestest.JPEGWithMeta(24, 16, 6)
	f.Add(good)
	f.Add(filestest.PlainJPEG(9, 9))
	f.Add(good[:len(good)/2])
	f.Add(append(append([]byte{}, good...), "trailing"...))
	f.Add([]byte{0xFF, 0xD8, 0xFF, 0xFF, 0xFE, 0x00, 0x04, 'h', 'i', 0xFF, 0xD9})
	f.Add([]byte{0xFF, 0xD8, 0xFF, 0xDA, 0x00, 0x02, 0x00, 0xFF, 0x00, 0xFF, 0xFF, 0xD0, 0xFF, 0xD9})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		var out bytes.Buffer
		err := StripJPEG(&out, bytes.NewReader(data))
		if err != nil {
			if !errors.Is(err, ErrMalformed) {
				t.Fatalf("error %v does not wrap ErrMalformed", err)
			}
			return
		}
		b := out.Bytes()
		if len(b) > len(data) {
			t.Fatalf("output (%d bytes) is larger than the input (%d)", len(b), len(data))
		}
		if !bytes.HasPrefix(b, []byte{0xFF, 0xD8}) || !bytes.HasSuffix(b, []byte{0xFF, 0xD9}) {
			t.Fatal("output lacks the start or end marker")
		}
		var again bytes.Buffer
		if err := StripJPEG(&again, bytes.NewReader(b)); err != nil || !bytes.Equal(again.Bytes(), b) {
			t.Fatalf("stripping is not idempotent: %v", err)
		}
		// Walk the header segments: none of the removed kinds is left.
		for pos := 2; pos+4 <= len(b); {
			if b[pos] != 0xFF {
				break
			}
			code := b[pos+1]
			if code == 0xE1 || code == 0xED || code == 0xFE {
				t.Fatalf("segment %#x survived", code)
			}
			if code == 0xDA || code == 0xD9 {
				break
			}
			pos += 2 + int(binary.BigEndian.Uint16(b[pos+2:]))
		}
	})
}

func FuzzStripPNG(f *testing.F) {
	f.Add(filestest.PNGWithMeta(12, 9))
	f.Add(filestest.PlainPNG(5, 5))
	f.Add(filestest.HugePNGHeader(100, 100))
	f.Add(filestest.PNGWithMeta(12, 9)[:60])
	f.Add([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x00IEND\xae\x42\x60\x82"))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		var out bytes.Buffer
		err := StripPNG(&out, bytes.NewReader(data))
		if err != nil {
			if !errors.Is(err, ErrMalformed) {
				t.Fatalf("error %v does not wrap ErrMalformed", err)
			}
			return
		}
		b := out.Bytes()
		if len(b) > len(data) {
			t.Fatalf("output (%d bytes) is larger than the input (%d)", len(b), len(data))
		}
		var again bytes.Buffer
		if err := StripPNG(&again, bytes.NewReader(b)); err != nil || !bytes.Equal(again.Bytes(), b) {
			t.Fatalf("stripping is not idempotent: %v", err)
		}
		for pos := 8; pos+8 <= len(b); {
			n := int(binary.BigEndian.Uint32(b[pos:]))
			typ := string(b[pos+4 : pos+8])
			if pngDropped[typ] {
				t.Fatalf("chunk %s survived", typ)
			}
			pos += 12 + n
		}
		if !bytes.HasSuffix(b, []byte("IEND\xae\x42\x60\x82")) && !bytes.Contains(b, []byte("IEND")) {
			t.Fatal("output has no IEND chunk")
		}
	})
}

func FuzzStripWebP(f *testing.F) {
	good := filestest.WebPWithMeta(7, 5)
	f.Add(good)
	f.Add(good[:len(good)-3])
	f.Add(append(append([]byte{}, good...), "trailing"...))
	f.Add([]byte("RIFF\x04\x00\x00\x00WEBP"))
	f.Add([]byte("RIFF\x0e\x00\x00\x00WEBPABCD\x01\x00\x00\x00x\x00"))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		var out bytes.Buffer
		err := StripWebP(&out, bytes.NewReader(data), int64(len(data)))
		if err != nil {
			if !errors.Is(err, ErrMalformed) {
				t.Fatalf("error %v does not wrap ErrMalformed", err)
			}
			return
		}
		b := out.Bytes()
		if got := int(binary.LittleEndian.Uint32(b[4:8])); got != len(b)-8 {
			t.Fatalf("RIFF size %d, file is %d bytes", got, len(b))
		}
		chunks, err := webpChunks(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			t.Fatalf("output does not parse: %v", err)
		}
		for _, c := range chunks {
			if webpStripped[c.fourcc] {
				t.Fatalf("chunk %q survived", c.fourcc)
			}
			if c.fourcc == "VP8X" && c.size >= 1 && b[c.off+8]&(webpFlagEXIF|webpFlagXMP) != 0 {
				t.Fatal("VP8X still announces EXIF or XMP")
			}
		}
		var again bytes.Buffer
		if err := StripWebP(&again, bytes.NewReader(b), int64(len(b))); err != nil || !bytes.Equal(again.Bytes(), b) {
			t.Fatalf("stripping is not idempotent: %v", err)
		}
	})
}

func FuzzSniff(f *testing.F) {
	f.Add(filestest.PlainJPEG(4, 4), "a.jpg")
	f.Add(filestest.PlainPNG(4, 4), "a.mp3")
	f.Add(filestest.SVGWithScript(), "logo.svg")
	f.Add([]byte("OggS\x00\x02"), "x.ogg")
	f.Add([]byte("\x1a\x45\xdf\xa3"), "x.weba")
	f.Add([]byte{}, "")
	f.Add([]byte("<html>"), "a.png\x00.jpg")
	f.Fuzz(func(t *testing.T, head []byte, name string) {
		k := Sniff(head, name)
		switch k.Category {
		case protocol.FileImage, protocol.FileVideo, protocol.FileAudio, protocol.FileOther:
		default:
			t.Fatalf("category %q", k.Category)
		}
		if k.Mime == "" || bytes.ContainsAny([]byte(k.Mime), "; \t\r\n,") {
			t.Fatalf("mime %q is empty or has parameters", k.Mime)
		}
		if k.Inline != IsInline(k.Category, k.Mime) {
			t.Fatalf("Inline = %v disagrees with the allowlist for %s %s", k.Inline, k.Category, k.Mime)
		}
		if k.Inline && inlineMimes[k.Mime] != k.Category {
			t.Fatalf("inline %s in category %s", k.Mime, k.Category)
		}
		if !k.Inline && k.Category != protocol.FileOther {
			t.Fatalf("a %s file that is not inline", k.Category)
		}
		if k.Format != FormatNone && (!k.Inline || k.Category != protocol.FileImage) {
			t.Fatalf("format %d for %+v", k.Format, k)
		}
		if k.Format != formatOf(k.Mime) {
			t.Fatalf("format %d for mime %s", k.Format, k.Mime)
		}
	})
}
