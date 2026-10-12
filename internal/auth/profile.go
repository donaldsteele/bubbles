package auth

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/donaldsteele/bubbles/internal/events"
	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// MaxStatusTextChars is the longest status text (the users table CHECK).
const MaxStatusTextChars = 100

// ProfileInput is a partial update of the caller's own profile: nil / false
// fields are left alone.
type ProfileInput struct {
	DisplayName *string
	StatusText  *string
	// SetAvatar makes AvatarFileID apply; a nil AvatarFileID then removes
	// the avatar.
	SetAvatar    bool
	AvatarFileID *string
	// Prefs replaces the whole preferences object; nil leaves it alone.
	Prefs json.RawMessage
}

// UpdateProfile changes the display name, status text, avatar and preferences
// of userID and returns the updated account. The avatar must be an image file
// the user uploaded. Rejected input is a *ValidationError. When something
// changed it appends a user.updated event in the same transaction.
func (s *Service) UpdateProfile(ctx context.Context, userID string, in ProfileInput) (*User, error) {
	var out *User
	err := s.db.Do(ctx, func(tx *store.Tx) error {
		cur, err := scanUser(tx.QueryRowContext(ctx, `SELECT `+userColumns("")+` FROM users WHERE id = ? AND deactivated_at IS NULL`, userID))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("load user: %w", err)
		}
		next := *cur
		fields := map[string]string{}
		if in.DisplayName != nil {
			name, why := normalizeDisplayName(*in.DisplayName)
			switch {
			case why != "":
				fields["display_name"] = why
			case name == "":
				next.DisplayName = cur.Username
			default:
				next.DisplayName = name
			}
		}
		if in.StatusText != nil {
			status := strings.TrimSpace(*in.StatusText)
			if utf8.RuneCountInString(status) > MaxStatusTextChars || strings.IndexFunc(status, unicode.IsControl) >= 0 || !utf8.ValidString(status) {
				fields["status_text"] = fmt.Sprintf("must be at most %d characters without control characters", MaxStatusTextChars)
			} else {
				next.StatusText = status
			}
		}
		if in.SetAvatar {
			if in.AvatarFileID == nil {
				next.AvatarFileID = nil
			} else if ok, err := ownsImage(ctx, tx, userID, *in.AvatarFileID); err != nil {
				return err
			} else if !ok {
				fields["avatar_file_id"] = "must be an image you uploaded"
			} else {
				next.AvatarFileID = in.AvatarFileID
			}
		}
		if in.Prefs != nil {
			prefs, why := normalizePrefs(in.Prefs)
			if why != "" {
				fields["prefs"] = why
			} else {
				next.Prefs = prefs
			}
		}
		if len(fields) > 0 {
			return &ValidationError{Fields: fields}
		}
		if in.DisplayName != nil && next.DisplayName != cur.DisplayName {
			if taken, err := nameTaken(tx, displayKey(next.DisplayName), userID); err != nil {
				return err
			} else if taken {
				return ErrNameTaken
			}
		}
		out = &next
		if next.DisplayName == cur.DisplayName && next.StatusText == cur.StatusText &&
			ptrEqual(next.AvatarFileID, cur.AvatarFileID) && bytes.Equal(next.Prefs, cur.Prefs) {
			out = cur
			return nil
		}
		var avatar any
		if next.AvatarFileID != nil {
			avatar = *next.AvatarFileID
		}
		_, err = tx.Exec(`UPDATE users SET display_name = ?, display_key = ?, status_text = ?, avatar_file_id = ?, prefs = ? WHERE id = ?`,
			next.DisplayName, displayKey(next.DisplayName), next.StatusText, avatar, string(next.Prefs), userID)
		if err != nil {
			return fmt.Errorf("update profile: %w", err)
		}
		payload, err := json.Marshal(publicUser(&next))
		if err != nil {
			return fmt.Errorf("encode user.updated: %w", err)
		}
		if _, err := events.Append(tx, events.Event{Type: events.TypeUserUpdated, Payload: payload}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ownsImage reports whether fileID is an image file uploaded by userID.
func ownsImage(ctx context.Context, tx *store.Tx, userID, fileID string) (bool, error) {
	var uploader, category string
	err := tx.QueryRowContext(ctx, `SELECT uploader_id, category FROM files WHERE id = ?`, fileID).Scan(&uploader, &category)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check avatar file: %w", err)
	}
	return uploader == userID && category == protocol.FileImage, nil
}

// normalizePrefs checks that raw is a JSON object of at most MaxPrefsBytes
// and returns it compacted.
func normalizePrefs(raw json.RawMessage) (json.RawMessage, string) {
	raw = bytes.TrimSpace(raw)
	if len(raw) > protocol.MaxPrefsBytes {
		return nil, fmt.Sprintf("must be at most %d bytes", protocol.MaxPrefsBytes)
	}
	if len(raw) == 0 || raw[0] != '{' || !json.Valid(raw) {
		return nil, "must be a JSON object"
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return nil, "must be a JSON object"
	}
	return buf.Bytes(), ""
}

func ptrEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// publicUser is the wire view of an account that user.updated carries.
func publicUser(u *User) protocol.User {
	return protocol.User{
		ID: u.ID, Username: u.Username, DisplayName: u.DisplayName, Kind: u.Kind, Role: string(u.Role),
		AvatarFileID: u.AvatarFileID, StatusText: u.StatusText, Deactivated: u.DeactivatedAt != nil,
		CreatedAt: protocol.FormatTime(u.CreatedAt),
	}
}
