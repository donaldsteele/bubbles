package core

// ChannelPath is the same-origin web path that opens a channel, or "" when id
// is not a plausible channel id (letters and digits, at most 64). The server's
// /auth/handoff accepts it as the `next` parameter.
func ChannelPath(id string) string {
	if id == "" || len(id) > 64 {
		return ""
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return ""
		}
	}
	return "/c/" + id
}
