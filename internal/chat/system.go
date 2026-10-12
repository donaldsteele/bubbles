package chat

import (
	"fmt"

	"github.com/donaldsteele/bubbles/internal/store"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// LoadMessageTx returns the wire view of a message inside the caller's
// transaction, so it sees the caller's uncommitted changes (an alert row
// updated in the same transaction, say). It performs no permission check;
// the caller has authorized access. A missing message is ErrNotFound.
func (s *Service) LoadMessageTx(tx *store.Tx, id string) (protocol.Message, error) {
	return loadMessage(tx.Context(), tx, id)
}

// SetBodyTx replaces the body of a message on behalf of the server itself,
// without an author or permission check: the alerts service re-renders the
// body of an alert message when the alert repeats. The body is validated like
// any message body, the plain text and mentions are refreshed (an alert or
// system message keeps none), and edited_at stays untouched (the author did
// not edit anything). It emits no event; the
// caller appends message.updated once the rest of its change is written. The
// message must exist and not be deleted.
func (s *Service) SetBodyTx(tx *store.Tx, messageID, body string) error {
	return s.setBody(tx, messageID, body, false)
}

// SetBodyQuietTx is SetBodyTx for a body that came from outside (a webhook):
// the message keeps no mentions, whatever the body says. SetBodyTx already
// keeps alert and system messages free of mentions, so the two are the same
// for them.
func (s *Service) SetBodyQuietTx(tx *store.Tx, messageID, body string) error {
	return s.setBody(tx, messageID, body, true)
}

func (s *Service) setBody(tx *store.Tx, messageID, body string, quiet bool) error {
	ctx := tx.Context()
	ref, err := loadRef(ctx, tx, messageID)
	if err != nil {
		return err
	}
	if ref.Deleted {
		return conflict("the message was deleted")
	}
	clean, err := validateBody(ref.Kind, body, 0)
	if err != nil {
		return err
	}
	if clean == ref.Body {
		return nil
	}
	parsed := parseBody(clean)
	if quiet || ref.Kind != protocol.MessageText {
		parsed.Mentions = Mentions{}
	}
	mentioned, err := resolveMentions(ctx, tx, ref.ChannelID, ref.AuthorID, parsed.Mentions.Usernames)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE messages SET body = ?, body_plain = ?, mention_channel = ?, mention_here = ? WHERE id = ?`,
		clean, parsed.Plain, boolInt(parsed.Mentions.Channel), boolInt(parsed.Mentions.Here), ref.ID)
	if err != nil {
		return fmt.Errorf("set message body: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM mentions WHERE message_id = ?`, ref.ID); err != nil {
		return fmt.Errorf("set message body: %w", err)
	}
	return insertMentions(tx, ref.ID, mentioned)
}
