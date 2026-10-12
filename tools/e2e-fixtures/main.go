// Command e2e-fixtures writes the binary fixtures used by the Playwright
// suite: a JPEG that carries EXIF GPS, XMP and IPTC metadata and a plain PNG.
//
//	go run ./tools/e2e-fixtures web/tests/e2e/fixtures
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/donaldsteele/bubbles/internal/files/filestest"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: e2e-fixtures <output-dir>")
		os.Exit(2)
	}
	dir := os.Args[1]
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	files := map[string][]byte{
		"gps.jpg":    filestest.JPEGWithMeta(96, 64, 1),
		"swatch.png": filestest.PlainPNG(120, 80),
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}
