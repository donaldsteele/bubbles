package files

import (
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxNameChars is the longest file name the files table accepts.
const MaxNameChars = 255

// CleanName turns the client's file name into a name that is safe to store,
// show and put into a header: only the last path element, valid UTF-8, no
// control or bidirectional-override characters, no trailing dots or blanks,
// at most 255 characters (the extension is kept). An unusable name becomes
// "file".
func CleanName(name string) string {
	name = strings.ToValidUTF8(name, "_")
	name = name[strings.LastIndexAny(name, `/\`)+1:]
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || isBidiControl(r) || r == utf8.RuneError {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(name), ". "))
	if name == "" {
		return "file"
	}
	if utf8.RuneCountInString(name) > MaxNameChars {
		ext := path.Ext(name)
		if utf8.RuneCountInString(ext) > 32 {
			ext = ""
		}
		keep := MaxNameChars - utf8.RuneCountInString(ext)
		runes := []rune(strings.TrimSuffix(name, ext))
		name = string(runes[:keep]) + ext
	}
	return name
}

func isBidiControl(r rune) bool {
	switch {
	case r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069:
		return true
	}
	return r == 0x200E || r == 0x200F || r == 0x061C
}
