package files

import (
	"net/http"
	"path"
	"strings"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// Format is a container format the service can strip and thumbnail.
type Format int

// The formats of processed images. FormatNone marks files that are stored
// as they arrive.
const (
	FormatNone Format = iota
	FormatJPEG
	FormatPNG
	FormatGIF
	FormatWebP
)

// sniffLen is how much of a file the content sniffer looks at.
const sniffLen = 512

// inlineMimes is the allowlist of types that are served inline with their own
// Content-Type, with the category each one belongs to. Everything else is
// served as application/octet-stream and an attachment.
var inlineMimes = map[string]string{
	"image/jpeg": protocol.FileImage,
	"image/png":  protocol.FileImage,
	"image/gif":  protocol.FileImage,
	"image/webp": protocol.FileImage,
	"video/mp4":  protocol.FileVideo,
	"video/webm": protocol.FileVideo,
	"audio/mpeg": protocol.FileAudio,
	"audio/ogg":  protocol.FileAudio,
	"audio/wav":  protocol.FileAudio,
	"audio/webm": protocol.FileAudio,
}

// extMimes maps the extensions of the inline types to their media type. It
// is the extension half of the sniff cross-check.
var extMimes = map[string]string{
	"jpg": "image/jpeg", "jpeg": "image/jpeg", "jpe": "image/jpeg", "jfif": "image/jpeg",
	"png": "image/png", "gif": "image/gif", "webp": "image/webp",
	"mp4": "video/mp4", "m4v": "video/mp4", "webm": "video/webm", "weba": "audio/webm",
	"mp3": "audio/mpeg", "ogg": "audio/ogg", "oga": "audio/ogg", "opus": "audio/ogg",
	"wav": "audio/wav",
}

// plainMimes names the common types of files that are stored as plain files.
// They are informational: such files are always served as downloads.
var plainMimes = map[string]string{
	"svg": "image/svg+xml", "pdf": "application/pdf", "heic": "image/heic", "heif": "image/heif",
	"txt": "text/plain", "csv": "text/csv", "json": "application/json", "xml": "application/xml",
	"html": "text/html", "htm": "text/html", "md": "text/markdown", "zip": "application/zip",
	"gz": "application/gzip", "tar": "application/x-tar", "7z": "application/x-7z-compressed",
	"doc": "application/msword", "xls": "application/vnd.ms-excel", "ppt": "application/vnd.ms-powerpoint",
	"docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	"xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	"pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	"mov":  "video/quicktime", "avi": "video/x-msvideo", "mkv": "video/x-matroska",
	"m4a": "audio/mp4", "flac": "audio/flac", "aac": "audio/aac",
}

// octetStream is the media type of files with no better type.
const octetStream = "application/octet-stream"

// Kind is what the sniffer decided about an upload.
type Kind struct {
	// Mime is the media type without parameters.
	Mime string
	// Category is one of protocol.FileImage, FileVideo, FileAudio, FileOther.
	Category string
	// Inline reports that Mime is on the inline allowlist and the file may be
	// served with its own Content-Type.
	Inline bool
	// Format is set for the image formats the service strips and thumbnails.
	Format Format
}

// IsInline reports whether a file of this category and media type is served
// inline. Files of category file are never inline, whatever their type.
func IsInline(category, mime string) bool {
	return category != protocol.FileOther && inlineMimes[mime] == category
}

// Sniff decides the type of a file from its first bytes (at most 512 are
// looked at) cross-checked with the extension of name. The content decides:
// a file that does not sniff as an inline type is a plain file whatever its
// extension says, and a file whose extension names a different category than
// its content (a ".png" holding audio) is demoted to a plain file too.
func Sniff(head []byte, name string) Kind {
	if len(head) > sniffLen {
		head = head[:sniffLen]
	}
	ext := extension(name)
	detected := normalizeSniffed(mediaType(http.DetectContentType(head)), ext)
	category, ok := inlineMimes[detected]
	if !ok {
		return plainKind(plainMimeOf(detected, ext))
	}
	if claimed := extMimes[ext]; claimed != "" && inlineMimes[claimed] != category {
		return plainKind(octetStream)
	}
	return Kind{Mime: detected, Category: category, Inline: true, Format: formatOf(detected)}
}

func plainKind(mime string) Kind {
	return Kind{Mime: mime, Category: protocol.FileOther}
}

// plainMimeOf is the informational type of a plain file: the extension's
// type when it is known, else the sniffed one, else octet-stream.
func plainMimeOf(detected, ext string) string {
	if m := plainMimes[ext]; m != "" {
		return m
	}
	if detected == "" || strings.ContainsAny(detected, ";, \t\r\n") {
		return octetStream
	}
	if _, inline := inlineMimes[detected]; inline {
		return octetStream
	}
	return detected
}

// normalizeSniffed maps the names http.DetectContentType uses to the ones of
// the allowlist, using the extension to tell apart containers that hold
// audio or video.
func normalizeSniffed(mime, ext string) string {
	switch mime {
	case "audio/wave", "audio/x-wav":
		return "audio/wav"
	case "application/ogg":
		if ext == "ogg" || ext == "oga" || ext == "opus" {
			return "audio/ogg"
		}
		return mime
	case "video/webm":
		if ext == "weba" {
			return "audio/webm"
		}
	}
	return mime
}

func formatOf(mime string) Format {
	switch mime {
	case "image/jpeg":
		return FormatJPEG
	case "image/png":
		return FormatPNG
	case "image/gif":
		return FormatGIF
	case "image/webp":
		return FormatWebP
	}
	return FormatNone
}

// mediaType drops the parameters of a Content-Type value.
func mediaType(ct string) string {
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return strings.ToLower(strings.TrimSpace(ct))
}

// extension is the lower-case extension of name without the dot.
func extension(name string) string {
	name = name[strings.LastIndexAny(name, `/\`)+1:]
	return strings.ToLower(strings.TrimPrefix(path.Ext(name), "."))
}
