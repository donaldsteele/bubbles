package files

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/donaldsteele/bubbles/internal/chat"
	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// Service implements chat.FileAttacher.
var _ chat.FileAttacher = (*Service)(nil)

// AttachTx binds uploaded files to a message inside the message's
// transaction. Every file must exist, have been uploaded by authorID and not
// be attached to another message yet; at most MaxPerMessage files are
// allowed. The attachments rows keep the order of fileIDs (position = index).
// The returned wire views are in the same order. A problem is reported as a
// chat validation error on file_ids that does not say which case applied, so
// ids of other users' files cannot be probed.
func (s *Service) AttachTx(tx *store.Tx, messageID, authorID string, fileIDs []string) ([]protocol.File, error) {
	if len(fileIDs) > MaxPerMessage {
		return nil, badFileIDs(fmt.Sprintf("at most %d files per message", MaxPerMessage))
	}
	ctx := tx.Context()
	out := make([]protocol.File, 0, len(fileIDs))
	seen := make(map[string]bool, len(fileIDs))
	for i, id := range fileIDs {
		if seen[id] {
			return nil, badFileIDs("file_ids must be unique")
		}
		seen[id] = true
		row, err := scanFile(tx.QueryRowContext(ctx, `SELECT `+fileCols+` FROM files WHERE id = ?`, id))
		if errors.Is(err, sql.ErrNoRows) || (err == nil && row.UploaderID != authorID) {
			return nil, badFileIDs(unusableFile)
		}
		if err != nil {
			return nil, fmt.Errorf("load file: %w", err)
		}
		var attached int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM attachments WHERE file_id = ?`, id).Scan(&attached); err != nil {
			return nil, fmt.Errorf("check attachment: %w", err)
		}
		if attached > 0 {
			return nil, badFileIDs(unusableFile)
		}
		if _, err := tx.Exec(`INSERT INTO attachments (message_id, file_id, position) VALUES (?, ?, ?)`, messageID, id, i); err != nil {
			return nil, fmt.Errorf("attach file: %w", err)
		}
		out = append(out, row.wire())
	}
	return out, nil
}

const unusableFile = "unknown file, already attached, or not uploaded by you"

func badFileIDs(reason string) error {
	return &chat.ValidationError{Fields: map[string]string{"file_ids": reason}}
}
