package files

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

var pngSignature = []byte("\x89PNG\r\n\x1a\n")

// pngDropped lists the ancillary chunks that carry metadata: EXIF, text
// comments in three encodings and the last-modification time.
var pngDropped = map[string]bool{"eXIf": true, "tEXt": true, "iTXt": true, "zTXt": true, "tIME": true}

// maxPNGChunk is the largest chunk length the PNG specification allows.
const maxPNGChunk = 1<<31 - 1

// StripPNG copies the PNG in src to dst without its eXIf, tEXt, iTXt, zTXt
// and tIME chunks. All other chunks, including the image data, are copied
// unchanged; bytes after the IEND chunk are dropped. A file without a valid
// signature, with an oversized chunk or without IEND returns an error
// wrapping ErrMalformed.
func StripPNG(dst io.Writer, src io.Reader) error {
	br := bufio.NewReaderSize(src, 64<<10)
	bw := bufio.NewWriterSize(dst, 64<<10)
	sig := make([]byte, len(pngSignature))
	if _, err := io.ReadFull(br, sig); err != nil || string(sig) != string(pngSignature) {
		return fmt.Errorf("%w: no PNG signature", ErrMalformed)
	}
	bw.Write(sig)
	var head [8]byte
	for {
		if _, err := io.ReadFull(br, head[:]); err != nil {
			return fmt.Errorf("%w: missing IEND chunk", ErrMalformed)
		}
		n := int64(binary.BigEndian.Uint32(head[:4]))
		typ := string(head[4:])
		if n > maxPNGChunk {
			return fmt.Errorf("%w: oversized chunk", ErrMalformed)
		}
		for _, c := range []byte(typ) {
			if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z') {
				return fmt.Errorf("%w: bad chunk type", ErrMalformed)
			}
		}
		// Data plus the 4-byte CRC.
		if pngDropped[typ] {
			if _, err := io.CopyN(io.Discard, br, n+4); err != nil {
				return fmt.Errorf("%w: truncated chunk", ErrMalformed)
			}
			continue
		}
		bw.Write(head[:])
		if _, err := io.CopyN(bw, br, n+4); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return fmt.Errorf("%w: truncated chunk", ErrMalformed)
			}
			return err
		}
		if typ == "IEND" {
			return bw.Flush()
		}
	}
}
