package onboarding

import (
	"bytes"
	"fmt"

	qrcode "github.com/skip2/go-qrcode"
)

// qrSVG renders content as a QR code SVG (one unit per module, including the
// quiet zone). Dark modules are drawn as one path of horizontal runs; colors
// are presentation attributes, so no inline style is needed.
func qrSVG(content string) ([]byte, error) {
	q, err := qrcode.New(content, qrcode.Medium)
	if err != nil {
		return nil, err
	}
	bm := q.Bitmap()
	n := len(bm)
	var b bytes.Buffer
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="crispEdges">`, n, n)
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="#ffffff"/><path fill="#000000" d="`, n, n)
	for y, row := range bm {
		for x := 0; x < len(row); {
			if !row[x] {
				x++
				continue
			}
			start := x
			for x < len(row) && row[x] {
				x++
			}
			fmt.Fprintf(&b, "M%d %dh%dv1h-%dz", start, y, x-start, x-start)
		}
	}
	b.WriteString(`"/></svg>`)
	return b.Bytes(), nil
}
