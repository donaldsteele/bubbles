package files

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"image"
	"image/draw"
	"image/jpeg"
	"io"
)

// jpegQuality is the quality of re-encoded JPEGs (rotated photos).
const jpegQuality = 92

const (
	exifHeader = "Exif\x00\x00"
	iccHeader  = "ICC_PROFILE\x00"
	// tagOrientation is the EXIF orientation tag in IFD0.
	tagOrientation = 0x0112
	// maxIFDEntries bounds the IFD0 scan of hostile files.
	maxIFDEntries = 1024
	// maxICCBytes bounds the colour profile carried over to a re-encoded JPEG.
	maxICCBytes = 1 << 20
)

// jpegMeta reads the header segments of a JPEG, up to the first scan, and
// returns the EXIF orientation (1 to 8; 1 when absent or unreadable) and the
// ICC profile segments, complete with marker and length, ready to be written
// into another JPEG. It never fails: an unreadable file has orientation 1 and
// no profile.
func jpegMeta(r io.Reader) (orientation int, icc []byte) {
	orientation = 1
	br := bufio.NewReaderSize(r, 64<<10)
	var soi [2]byte
	if _, err := io.ReadFull(br, soi[:]); err != nil || soi != [2]byte{0xFF, jpegSOI} {
		return 1, nil
	}
	foundExif := false
	for {
		code, err := readJPEGMarker(br, false)
		if err != nil || code == jpegSOS || code == jpegEOI {
			return orientation, icc
		}
		if code == 0x01 || (code >= 0xD0 && code <= 0xD8) {
			continue
		}
		var lb [2]byte
		if _, err := io.ReadFull(br, lb[:]); err != nil {
			return orientation, icc
		}
		n := int(lb[0])<<8 | int(lb[1])
		if n < 2 {
			return orientation, icc
		}
		if code != jpegAPP1 && code != jpegAPP2 {
			if _, err := br.Discard(n - 2); err != nil {
				return orientation, icc
			}
			continue
		}
		payload := make([]byte, n-2)
		if _, err := io.ReadFull(br, payload); err != nil {
			return orientation, icc
		}
		switch {
		case code == jpegAPP1 && !foundExif && bytes.HasPrefix(payload, []byte(exifHeader)):
			foundExif = true
			orientation = tiffOrientation(payload[len(exifHeader):])
		case code == jpegAPP2 && bytes.HasPrefix(payload, []byte(iccHeader)) && len(icc)+len(payload) < maxICCBytes:
			icc = append(icc, 0xFF, code, lb[0], lb[1])
			icc = append(icc, payload...)
		}
	}
}

// tiffOrientation returns the orientation stored in the first IFD of a TIFF
// structure, or 1.
func tiffOrientation(t []byte) int {
	if len(t) < 8 {
		return 1
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	if bo.Uint16(t[2:4]) != 42 {
		return 1
	}
	off := int64(bo.Uint32(t[4:8]))
	if off+2 > int64(len(t)) {
		return 1
	}
	n := int(bo.Uint16(t[off : off+2]))
	if n > maxIFDEntries {
		n = maxIFDEntries
	}
	for i := range n {
		e := off + 2 + int64(i)*12
		if e+12 > int64(len(t)) {
			return 1
		}
		if bo.Uint16(t[e:e+2]) != tagOrientation {
			continue
		}
		// type SHORT (3), count 1: the value sits in the first two bytes.
		if bo.Uint16(t[e+2:e+4]) != 3 || bo.Uint32(t[e+4:e+8]) != 1 {
			return 1
		}
		if v := int(bo.Uint16(t[e+8 : e+10])); v >= 1 && v <= 8 {
			return v
		}
		return 1
	}
	return 1
}

// orient applies EXIF orientation o (2 to 8) to img and returns the upright
// picture as a new RGBA image. Any other value returns an unrotated copy.
func orient(img image.Image, o int) *image.RGBA {
	b := img.Bounds()
	src, ok := img.(*image.RGBA)
	if !ok || src.Rect.Min != (image.Point{}) {
		src = image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
		draw.Draw(src, src.Bounds(), img, b.Min, draw.Src)
	}
	w, h := src.Rect.Dx(), src.Rect.Dy()
	dw, dh := w, h
	if o >= 5 && o <= 8 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for dy := range dh {
		row := dst.Pix[dy*dst.Stride : dy*dst.Stride+dw*4]
		for dx := range dw {
			var sx, sy int
			switch o {
			case 2:
				sx, sy = w-1-dx, dy
			case 3:
				sx, sy = w-1-dx, h-1-dy
			case 4:
				sx, sy = dx, h-1-dy
			case 5:
				sx, sy = dy, dx
			case 6:
				sx, sy = dy, h-1-dx
			case 7:
				sx, sy = w-1-dy, h-1-dx
			case 8:
				sx, sy = w-1-dy, dx
			default:
				sx, sy = dx, dy
			}
			copy(row[dx*4:dx*4+4], src.Pix[sy*src.Stride+sx*4:sy*src.Stride+sx*4+4])
		}
	}
	return dst
}

// encodeJPEG writes img as a JPEG at jpegQuality, with the ICC profile
// segments in icc (as returned by jpegMeta) placed right after the start
// marker so the colours survive the re-encoding.
func encodeJPEG(w io.Writer, img image.Image, icc []byte) error {
	if len(icc) == 0 {
		return jpeg.Encode(w, img, &jpeg.Options{Quality: jpegQuality})
	}
	if _, err := w.Write(append([]byte{0xFF, jpegSOI}, icc...)); err != nil {
		return err
	}
	return jpeg.Encode(&skipWriter{w: w, skip: 2}, img, &jpeg.Options{Quality: jpegQuality})
}

// skipWriter drops the first skip bytes written to it.
type skipWriter struct {
	w    io.Writer
	skip int
}

func (s *skipWriter) Write(p []byte) (int, error) {
	n := len(p)
	if s.skip > 0 {
		d := min(s.skip, len(p))
		s.skip -= d
		p = p[d:]
	}
	if len(p) > 0 {
		if _, err := s.w.Write(p); err != nil {
			return 0, err
		}
	}
	return n, nil
}
