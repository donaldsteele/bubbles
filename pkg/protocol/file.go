package protocol

import "net/url"

// File categories.
const (
	FileImage = "image"
	FileVideo = "video"
	FileAudio = "audio"
	FileOther = "file"
)

// AvatarFileName is the name segment of an avatar URL; the server looks files
// up by id only, so the name is cosmetic.
const AvatarFileName = "avatar"

// File is an uploaded file. It is the answer of POST /files and the element
// of Message.Files. Name is the sanitized original file name. MetaStripped is
// false for files whose metadata could not be removed (HEIC, video, audio,
// documents and images that were not processed).
type File struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Size         int64   `json:"size"`
	Mime         string  `json:"mime"`
	Category     string  `json:"category"`
	Width        *int    `json:"width"`
	Height       *int    `json:"height"`
	HasThumb     bool    `json:"has_thumb"`
	MetaStripped bool    `json:"meta_stripped"`
	URL          string  `json:"url"`
	ThumbURL     *string `json:"thumb_url"`
}

// FileURL is the URL path a file is served from.
func FileURL(id, name string) string {
	return "/api/v1/files/" + url.PathEscape(id) + "/" + url.PathEscape(name)
}

// FileThumbURL is the URL path of a file's thumbnail.
func FileThumbURL(id, name string) string { return FileURL(id, name) + "?thumb=1" }

// AvatarURL is the URL path of a user's avatar image.
func AvatarURL(fileID string) string { return FileURL(fileID, AvatarFileName) }
