package files

import (
	"bufio"
	"errors"
	"fmt"
	"io"
)

// ErrMalformed reports that a file is not a well-formed image of the format
// it claims to be. The service stores such files as plain downloads.
var ErrMalformed = errors.New("malformed image")

// JPEG markers the stripper looks at.
const (
	jpegSOS  = 0xDA
	jpegEOI  = 0xD9
	jpegSOI  = 0xD8
	jpegAPP1 = 0xE1 // EXIF and XMP
	jpegAPP2 = 0xE2 // ICC profile
	jpegAPPD = 0xED // APP13: Photoshop IPTC
	jpegCOM  = 0xFE
)

// StripJPEG copies the JPEG in src to dst without its APP1 (EXIF, XMP), APP13
// (IPTC) and COM (comment) segments. The compressed image data is copied
// byte for byte, so the picture is not re-encoded. Bytes after the EOI marker
// (thumbnails, embedded videos of motion photos) are dropped. A truncated or
// otherwise malformed file returns an error wrapping ErrMalformed.
func StripJPEG(dst io.Writer, src io.Reader) error {
	br := bufio.NewReaderSize(src, 64<<10)
	bw := bufio.NewWriterSize(dst, 64<<10)
	var soi [2]byte
	if _, err := io.ReadFull(br, soi[:]); err != nil || soi != [2]byte{0xFF, jpegSOI} {
		return fmt.Errorf("%w: no JPEG start marker", ErrMalformed)
	}
	bw.Write(soi[:])
	scanned := false
	for {
		code, err := readJPEGMarker(br, scanned)
		scanned = false
		if err != nil {
			return err
		}
		switch {
		case code == jpegEOI:
			bw.Write([]byte{0xFF, jpegEOI})
			return bw.Flush()
		case code == 0x01 || (code >= 0xD0 && code <= 0xD7):
			bw.Write([]byte{0xFF, code})
			continue
		case code == 0x00 || code == jpegSOI:
			return fmt.Errorf("%w: unexpected marker %#x", ErrMalformed, code)
		}
		var lb [2]byte
		if _, err := io.ReadFull(br, lb[:]); err != nil {
			return fmt.Errorf("%w: truncated segment", ErrMalformed)
		}
		n := int64(lb[0])<<8 | int64(lb[1])
		if n < 2 {
			return fmt.Errorf("%w: bad segment length", ErrMalformed)
		}
		if code == jpegAPP1 || code == jpegAPPD || code == jpegCOM {
			if _, err := io.CopyN(io.Discard, br, n-2); err != nil {
				return fmt.Errorf("%w: truncated segment", ErrMalformed)
			}
			continue
		}
		bw.Write([]byte{0xFF, code, lb[0], lb[1]})
		if _, err := io.CopyN(bw, br, n-2); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return fmt.Errorf("%w: truncated segment", ErrMalformed)
			}
			return err
		}
		if code == jpegSOS {
			if err := copyJPEGScan(br, bw); err != nil {
				return err
			}
			scanned = true
		}
	}
}

// readJPEGMarker returns the code of the next marker. prefixed reports that
// the 0xFF that starts it was consumed already.
func readJPEGMarker(br *bufio.Reader, prefixed bool) (byte, error) {
	if !prefixed {
		b, err := br.ReadByte()
		if err != nil {
			return 0, fmt.Errorf("%w: missing end marker", ErrMalformed)
		}
		if b != 0xFF {
			return 0, fmt.Errorf("%w: expected a marker", ErrMalformed)
		}
	}
	for {
		b, err := br.ReadByte()
		if err != nil {
			return 0, fmt.Errorf("%w: missing end marker", ErrMalformed)
		}
		if b != 0xFF {
			return b, nil
		}
	}
}

// copyJPEGScan copies entropy-coded scan data up to the next marker. It
// consumes that marker's 0xFF prefix (and any fill bytes before it) and
// leaves the marker code unread.
func copyJPEGScan(br *bufio.Reader, bw *bufio.Writer) error {
	for {
		chunk, err := br.ReadSlice(0xFF)
		if errors.Is(err, bufio.ErrBufferFull) {
			bw.Write(chunk)
			continue
		}
		if err != nil {
			return fmt.Errorf("%w: truncated image data", ErrMalformed)
		}
		bw.Write(chunk[:len(chunk)-1])
		for {
			next, err := br.Peek(1)
			if err != nil {
				return fmt.Errorf("%w: truncated image data", ErrMalformed)
			}
			b := next[0]
			if b == 0xFF {
				br.Discard(1)
				continue
			}
			if b == 0x00 || (b >= 0xD0 && b <= 0xD7) {
				bw.Write([]byte{0xFF, b})
				br.Discard(1)
				break
			}
			return nil
		}
	}
}
