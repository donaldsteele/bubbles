// Command distzip writes a zip archive that stores each named file at the
// archive root under its base name. It replaces the external zip binary so
// release scripts run the same on Windows, macOS and Linux.
//
// Usage:
//
//	go run ./tools/distzip -out X.zip file1 file2 ...
package main

import (
	"archive/zip"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func main() {
	out := flag.String("out", "", "path of the zip archive to create (required)")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: distzip -out X.zip file1 [file2 ...]")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *out == "" || flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}
	if err := writeZip(*out, flag.Args()); err != nil {
		fmt.Fprintf(os.Stderr, "distzip: %v\n", err)
		os.Exit(1)
	}
}

// writeZip creates dst containing one flat entry per file. Duplicate base
// names are rejected because they would produce ambiguous archives.
func writeZip(dst string, files []string) (err error) {
	if err := checkUniqueBaseNames(files); err != nil {
		return err
	}

	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}()

	zw := zip.NewWriter(f)
	for _, name := range files {
		if err := addFile(zw, name); err != nil {
			_ = zw.Close()
			return err
		}
	}
	return zw.Close()
}

func checkUniqueBaseNames(files []string) error {
	seen := make(map[string]string, len(files))
	for _, name := range files {
		base := filepath.Base(name)
		if prev, dup := seen[base]; dup {
			return fmt.Errorf("duplicate entry name %q (from %s and %s)", base, prev, name)
		}
		seen[base] = name
	}
	return nil
}

func addFile(zw *zip.Writer, path string) error {
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}

	hdr, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	hdr.Name = filepath.Base(path)
	hdr.Method = zip.Deflate

	w, err := zw.CreateHeader(hdr)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, in)
	return err
}
