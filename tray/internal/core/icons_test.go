package core

import (
	"bytes"
	"image/png"
	"testing"
)

func TestIconPNGStates(t *testing.T) {
	seen := map[string]Indicator{}
	for _, ind := range []Indicator{IndicatorNormal, IndicatorUnread, IndicatorCritical} {
		b := IconPNG(ind)
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil {
			t.Fatalf("state %d: %v", ind, err)
		}
		if r := img.Bounds(); r.Dx() != IconSize || r.Dy() != IconSize {
			t.Errorf("state %d: size %v", ind, r)
		}
		if _, _, _, a := img.At(15, 14).RGBA(); a == 0 {
			t.Errorf("state %d: the bubble centre is transparent", ind)
		}
		if prev, dup := seen[string(b)]; dup {
			t.Errorf("states %d and %d render the same icon", prev, ind)
		}
		seen[string(b)] = ind
	}
}
