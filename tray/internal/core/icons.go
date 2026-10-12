package core

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
)

// IconSize is the edge length of the generated tray icons in pixels.
const IconSize = 32

// Icon colours: the bubbles accent plus the status colours (never colour
// alone: the unread and critical states also change the shape).
var (
	iconBody     = color.NRGBA{R: 0xfb, G: 0x92, B: 0x3c, A: 0xff}
	iconBodyDark = color.NRGBA{R: 0xc2, G: 0x41, B: 0x0c, A: 0xff}
	iconUnread   = color.NRGBA{R: 0x3b, G: 0x82, B: 0xf6, A: 0xff}
	iconCritical = color.NRGBA{R: 0xdc, G: 0x26, B: 0x26, A: 0xff}
	iconWhite    = color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
)

// IconPNG renders the tray icon for an indicator state as a PNG: a speech
// bubble; with a round dot when there is unread content and a square badge
// with an exclamation mark for a critical alert.
func IconPNG(ind Indicator) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, IconSize, IconSize))
	const cx, cy, r = 15.0, 14.0, 12.0
	for y := 0; y < IconSize; y++ {
		for x := 0; x < IconSize; x++ {
			fx, fy := float64(x)+0.5, float64(y)+0.5
			dx, dy := fx-cx, fy-cy
			d2 := dx*dx + dy*dy
			// Tail of the bubble: a small triangle at the lower left.
			tail := fy >= 24 && fy <= 30 && fx >= 5 && fx-5 <= (30-fy)*1.4
			switch {
			case d2 <= r*r && d2 > (r-2)*(r-2):
				img.SetNRGBA(x, y, iconBodyDark)
			case d2 <= (r-2)*(r-2):
				img.SetNRGBA(x, y, iconBody)
			case tail:
				img.SetNRGBA(x, y, iconBody)
			}
		}
	}
	switch ind {
	case IndicatorUnread:
		fillCircle(img, 25, 7, 6, iconUnread)
	case IndicatorCritical:
		fillRect(img, 19, 1, 31, 13, iconCritical)
		fillRect(img, 24, 3, 26, 9, iconWhite)
		fillRect(img, 24, 10, 26, 12, iconWhite)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil
	}
	return buf.Bytes()
}

func fillCircle(img *image.NRGBA, cx, cy, r float64, c color.NRGBA) {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			dx, dy := float64(x)+0.5-cx, float64(y)+0.5-cy
			if dx*dx+dy*dy <= r*r {
				img.SetNRGBA(x, y, c)
			}
		}
	}
}

func fillRect(img *image.NRGBA, x0, y0, x1, y1 int, c color.NRGBA) {
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
}
