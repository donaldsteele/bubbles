package files

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
)

// webpStripped are the metadata chunks of a WebP file.
var webpStripped = map[string]bool{"EXIF": true, "XMP ": true}

// VP8X feature flags that announce the chunks StripWebP removes.
const (
	webpFlagEXIF = 0x08
	webpFlagXMP  = 0x04
)

// maxWebPChunks bounds the chunk list built for a hostile file.
const maxWebPChunks = 1 << 17

type webpChunk struct {
	fourcc string
	off    int64 // offset of the chunk header
	size   uint32
}

func (c webpChunk) padded() int64 { return int64(c.size) + int64(c.size&1) }

// webpChunks parses the RIFF container of src (size bytes long) and returns
// its chunks. The RIFF size must not exceed the file; bytes after it are
// ignored.
func webpChunks(src io.ReaderAt, size int64) ([]webpChunk, error) {
	var hdr [12]byte
	if size < 12 {
		return nil, fmt.Errorf("%w: too short for WebP", ErrMalformed)
	}
	if _, err := src.ReadAt(hdr[:], 0); err != nil || string(hdr[:4]) != "RIFF" || string(hdr[8:12]) != "WEBP" {
		return nil, fmt.Errorf("%w: no WebP header", ErrMalformed)
	}
	end := int64(binary.LittleEndian.Uint32(hdr[4:8])) + 8
	if end > size {
		return nil, fmt.Errorf("%w: truncated WebP", ErrMalformed)
	}
	var chunks []webpChunk
	for pos := int64(12); pos+8 <= end; {
		var ch [8]byte
		if _, err := src.ReadAt(ch[:], pos); err != nil {
			return nil, fmt.Errorf("%w: truncated chunk header", ErrMalformed)
		}
		c := webpChunk{fourcc: string(ch[:4]), off: pos, size: binary.LittleEndian.Uint32(ch[4:])}
		if pos+8+int64(c.size) > end {
			return nil, fmt.Errorf("%w: chunk exceeds the file", ErrMalformed)
		}
		if len(chunks) >= maxWebPChunks {
			return nil, fmt.Errorf("%w: too many chunks", ErrMalformed)
		}
		chunks = append(chunks, c)
		pos += 8 + c.padded()
	}
	if len(chunks) == 0 {
		return nil, fmt.Errorf("%w: no chunks", ErrMalformed)
	}
	return chunks, nil
}

// StripWebP copies the WebP file in src (size bytes) to dst without its EXIF
// and XMP chunks and clears the matching flags of the VP8X header, fixing the
// RIFF size. Image data and the colour profile are copied unchanged. A file
// that is not a well-formed RIFF/WebP container returns an error wrapping
// ErrMalformed.
func StripWebP(dst io.Writer, src io.ReaderAt, size int64) error {
	chunks, err := webpChunks(src, size)
	if err != nil {
		return err
	}
	kept := chunks[:0:0]
	total := int64(4)
	for _, c := range chunks {
		if webpStripped[c.fourcc] {
			continue
		}
		kept = append(kept, c)
		total += 8 + c.padded()
	}
	if len(kept) == 0 || total > math.MaxUint32 {
		return fmt.Errorf("%w: nothing left to store", ErrMalformed)
	}
	out := make([]byte, 0, 12)
	out = append(out, "RIFF"...)
	out = binary.LittleEndian.AppendUint32(out, uint32(total))
	out = append(out, "WEBP"...)
	if _, err := dst.Write(out); err != nil {
		return err
	}
	var pad = []byte{0}
	for _, c := range kept {
		var head [8]byte
		copy(head[:4], c.fourcc)
		binary.LittleEndian.PutUint32(head[4:], c.size)
		if _, err := dst.Write(head[:]); err != nil {
			return err
		}
		body := io.NewSectionReader(src, c.off+8, int64(c.size))
		if c.fourcc == "VP8X" && c.size >= 1 {
			var flags [1]byte
			if _, err := body.ReadAt(flags[:], 0); err != nil {
				return fmt.Errorf("%w: truncated VP8X chunk", ErrMalformed)
			}
			flags[0] &^= webpFlagEXIF | webpFlagXMP
			if _, err := dst.Write(flags[:]); err != nil {
				return err
			}
			body = io.NewSectionReader(src, c.off+9, int64(c.size)-1)
		}
		if n, err := io.Copy(dst, body); err != nil {
			return err
		} else if n != body.Size() {
			return fmt.Errorf("%w: truncated chunk", ErrMalformed)
		}
		if c.size&1 == 1 {
			if _, err := dst.Write(pad); err != nil {
				return err
			}
		}
	}
	return nil
}

// webpCanvas returns the canvas size announced by the VP8X header of a WebP
// file. It is how the size of animated files is found: the standard decoder
// only reads still images.
func webpCanvas(src io.ReaderAt, size int64) (w, h int, ok bool) {
	chunks, err := webpChunks(src, size)
	if err != nil || chunks[0].fourcc != "VP8X" || chunks[0].size < 10 {
		return 0, 0, false
	}
	var p [10]byte
	if _, err := src.ReadAt(p[:], chunks[0].off+8); err != nil {
		return 0, 0, false
	}
	w = 1 + (int(p[4]) | int(p[5])<<8 | int(p[6])<<16)
	h = 1 + (int(p[7]) | int(p[8])<<8 | int(p[9])<<16)
	return w, h, true
}
