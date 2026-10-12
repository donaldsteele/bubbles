package files

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"

	xdraw "golang.org/x/image/draw"

	// Decoders for image.Decode and image.DecodeConfig.
	_ "golang.org/x/image/webp"
	_ "image/gif"
)

const (
	// ThumbEdge is the long edge of a thumbnail in pixels. Images that are
	// not larger than this get no thumbnail.
	ThumbEdge = 640
	// MaxPixels is the largest image (width x height) the service decodes.
	// Larger images are stored as they arrive and served as downloads.
	MaxPixels = 50_000_000

	thumbJPEGQuality = 80
)

// thumbSize returns the size of the thumbnail of a w x h image, or false when
// the image fits in ThumbEdge already.
func thumbSize(w, h int) (tw, th int, ok bool) {
	long := max(w, h)
	if long <= ThumbEdge || w <= 0 || h <= 0 {
		return 0, 0, false
	}
	tw = max(1, (w*ThumbEdge+long/2)/long)
	th = max(1, (h*ThumbEdge+long/2)/long)
	return tw, th, true
}

// makeThumb scales img down to ThumbEdge on its long edge with the
// Catmull-Rom filter. The result is a PNG when the image has transparency
// and a JPEG at quality 80 otherwise.
func makeThumb(img image.Image) (data []byte, mime string, err error) {
	b := img.Bounds()
	tw, th, ok := thumbSize(b.Dx(), b.Dy())
	if !ok {
		tw, th = b.Dx(), b.Dy()
	}
	var buf bytes.Buffer
	if hasAlpha(img) {
		dst := image.NewNRGBA(image.Rect(0, 0, tw, th))
		xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, b, xdraw.Over, nil)
		if err := png.Encode(&buf, dst); err != nil {
			return nil, "", err
		}
		return buf.Bytes(), "image/png", nil
	}
	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, b, xdraw.Src, nil)
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: thumbJPEGQuality}); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), "image/jpeg", nil
}

// hasAlpha reports whether img has translucent or transparent pixels.
func hasAlpha(img image.Image) bool {
	if o, ok := img.(interface{ Opaque() bool }); ok {
		return !o.Opaque()
	}
	return false
}
