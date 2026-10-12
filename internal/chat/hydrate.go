package chat

import (
	"context"

	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// HydrateMessagesUnchecked loads the wire view of the messages with the given ids, in
// that order, skipping ids that do not exist.
//
// It performs no permission check (hence the name): the caller (internal/search,
// internal/alerts) has already restricted ids to messages the actor may see.
func (s *Service) HydrateMessagesUnchecked(ctx context.Context, ids []string) ([]protocol.Message, error) {
	return loadMessageList(ctx, s.db.Read(), ids)
}
