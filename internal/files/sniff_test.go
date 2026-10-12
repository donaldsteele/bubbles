package files

import (
	"strings"
	"testing"

	"github.com/donaldsteele/bubbles/internal/files/filestest"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

func TestFilesSniff(t *testing.T) {
	mp4 := append([]byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom"), make([]byte, 16)...)
	webm := append([]byte("\x1a\x45\xdf\xa3\x93\x42\x82\x88matroska"), make([]byte, 16)...)
	mp3 := append([]byte("ID3\x03\x00\x00\x00\x00\x00\x00"), make([]byte, 32)...)
	ogg := append([]byte("OggS\x00\x02"), make([]byte, 32)...)
	wav := append([]byte("RIFF\x24\x00\x00\x00WAVEfmt "), make([]byte, 32)...)
	heic := append([]byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00mif1heic"), make([]byte, 16)...)

	tests := []struct {
		name     string
		head     []byte
		file     string
		mime     string
		category string
		inline   bool
		format   Format
	}{
		{"jpeg", filestest.PlainJPEG(8, 8), "a.jpg", "image/jpeg", protocol.FileImage, true, FormatJPEG},
		{"png", filestest.PlainPNG(8, 8), "a.png", "image/png", protocol.FileImage, true, FormatPNG},
		{"gif", filestest.GIF(8, 8), "a.gif", "image/gif", protocol.FileImage, true, FormatGIF},
		{"webp", filestest.WebPWithMeta(4, 4), "a.webp", "image/webp", protocol.FileImage, true, FormatWebP},
		{"jpeg named png keeps the content type", filestest.PlainJPEG(8, 8), "a.png", "image/jpeg", protocol.FileImage, true, FormatJPEG},
		{"jpeg without extension", filestest.PlainJPEG(8, 8), "photo", "image/jpeg", protocol.FileImage, true, FormatJPEG},
		{"mp4", mp4, "clip.mp4", "video/mp4", protocol.FileVideo, true, FormatNone},
		{"webm video", webm, "clip.webm", "video/webm", protocol.FileVideo, true, FormatNone},
		{"webm audio", webm, "clip.weba", "audio/webm", protocol.FileAudio, true, FormatNone},
		{"mp3", mp3, "song.mp3", "audio/mpeg", protocol.FileAudio, true, FormatNone},
		{"ogg", ogg, "song.ogg", "audio/ogg", protocol.FileAudio, true, FormatNone},
		{"ogg without audio extension", ogg, "movie.ogv", "application/ogg", protocol.FileOther, false, FormatNone},
		{"wav", wav, "sound.wav", "audio/wav", protocol.FileAudio, true, FormatNone},
		{"audio named png is demoted", mp3, "a.png", octetStream, protocol.FileOther, false, FormatNone},
		{"png named mp3 is demoted", filestest.PlainPNG(8, 8), "a.mp3", octetStream, protocol.FileOther, false, FormatNone},
		{"html named png is a plain file", []byte("<html><script>alert(1)</script></html>"), "a.png", "text/html", protocol.FileOther, false, FormatNone},
		{"svg", filestest.SVGWithScript(), "logo.svg", "image/svg+xml", protocol.FileOther, false, FormatNone},
		{"heic", heic, "IMG_0001.HEIC", "image/heic", protocol.FileOther, false, FormatNone},
		{"pdf", []byte("%PDF-1.7\n%..."), "doc.pdf", "application/pdf", protocol.FileOther, false, FormatNone},
		{"empty", nil, "empty.txt", "text/plain", protocol.FileOther, false, FormatNone},
		{"unknown binary", []byte{0, 1, 2, 3, 4, 5, 6, 7}, "data.bin", octetStream, protocol.FileOther, false, FormatNone},
	}
	for _, tc := range tests {
		got := Sniff(tc.head, tc.file)
		if got.Mime != tc.mime || got.Category != tc.category || got.Inline != tc.inline || got.Format != tc.format {
			t.Errorf("%s: Sniff = %+v, want mime=%s category=%s inline=%v format=%d", tc.name, got, tc.mime, tc.category, tc.inline, tc.format)
		}
	}
}

func TestFilesSniffOnlyLooksAtTheFirst512Bytes(t *testing.T) {
	head := append(filestest.PlainJPEG(8, 8), []byte(strings.Repeat("x", 4096))...)
	if got := Sniff(head, "a.jpg"); got.Mime != "image/jpeg" {
		t.Errorf("mime = %s", got.Mime)
	}
}

func TestFilesIsInlineNeedsCategoryAndAllowlistedType(t *testing.T) {
	tests := []struct {
		category, mime string
		want           bool
	}{
		{protocol.FileImage, "image/png", true},
		{protocol.FileVideo, "video/mp4", true},
		{protocol.FileAudio, "audio/mpeg", true},
		{protocol.FileOther, "image/png", false},
		{protocol.FileImage, "image/svg+xml", false},
		{protocol.FileImage, "text/html", false},
		{protocol.FileVideo, "image/png", false},
	}
	for _, tc := range tests {
		if got := IsInline(tc.category, tc.mime); got != tc.want {
			t.Errorf("IsInline(%s, %s) = %v, want %v", tc.category, tc.mime, got, tc.want)
		}
	}
}

func TestFilesCleanName(t *testing.T) {
	long := strings.Repeat("é", 300)
	tests := []struct{ in, want string }{
		{"photo.jpg", "photo.jpg"},
		{"../../etc/passwd", "passwd"},
		{`C:\Users\me\secret.txt`, "secret.txt"},
		{"", "file"},
		{".", "file"},
		{"..", "file"},
		{"  ...  ", "file"},
		{"a\x00b\nc.txt", "abc.txt"},
		{"report\u202Efdp.exe", "reportfdp.exe"},
		{"name.", "name"},
		{"bad\xffutf8.txt", "bad_utf8.txt"},
		{".bashrc", ".bashrc"},
		{"日本語.png", "日本語.png"},
	}
	for _, tc := range tests {
		if got := CleanName(tc.in); got != tc.want {
			t.Errorf("CleanName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	got := CleanName(long + ".png")
	if n := len([]rune(got)); n != MaxNameChars || !strings.HasSuffix(got, ".png") {
		t.Errorf("long name has %d chars, suffix kept = %v", n, strings.HasSuffix(got, ".png"))
	}
}

func TestFilesContentDisposition(t *testing.T) {
	tests := []struct{ kind, name, want string }{
		{"inline", "photo.jpg", `inline; filename="photo.jpg"`},
		{"attachment", `we"ird\name%.txt`, `attachment; filename="we_ird_name_.txt"`},
		{"attachment", "résumé 日本.pdf", `attachment; filename="r_sum_ __.pdf"; filename*=UTF-8''r%C3%A9sum%C3%A9%20%E6%97%A5%E6%9C%AC.pdf`},
	}
	for _, tc := range tests {
		if got := contentDisposition(tc.kind, tc.name); got != tc.want {
			t.Errorf("contentDisposition(%s, %q)\n got %s\nwant %s", tc.kind, tc.name, got, tc.want)
		}
	}
}
