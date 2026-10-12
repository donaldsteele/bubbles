package wsgateway

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/donaldsteele/bubbles/internal/events"
	"github.com/donaldsteele/bubbles/pkg/protocol"
)

// replayBatch is the number of events read per replay query.
const replayBatch = 500

// logState is the instance identity and event-log bounds read in one
// consistent snapshot when a connection starts.
type logState struct {
	instanceID string
	epoch      int64
	oldest     int64 // oldest retained seq, 0 when the log is empty
	head       int64 // sequence head
}

func loadState(ctx context.Context, db *sql.DB) (logState, error) {
	var st logState
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return st, fmt.Errorf("load realtime state: %w", err)
	}
	defer tx.Rollback()
	if err := tx.QueryRowContext(ctx, `SELECT id, epoch FROM instance`).Scan(&st.instanceID, &st.epoch); err != nil {
		return st, fmt.Errorf("load realtime state: %w", err)
	}
	st.oldest, st.head, err = events.Bounds(ctx, tx)
	return st, err
}

// resumeOutcome decides hello.resume and whether events must be replayed:
//
//   - instance given and different -> instance_changed (no replay);
//   - epoch given and different -> resync_required (no replay);
//   - no resume -> ok, nothing to replay;
//   - resume ahead of the head -> resync_required;
//   - resume at the head -> ok;
//   - any event after resume already pruned -> resync_required;
//   - otherwise ok, replay (resume, head].
func resumeOutcome(q query, st logState) (string, bool) {
	switch {
	case q.instance != "" && q.instance != st.instanceID:
		return protocol.ResumeInstanceChanged, false
	case q.hasEpoch && q.epoch != st.epoch:
		return protocol.ResumeResyncRequired, false
	case !q.hasResume:
		return protocol.ResumeOK, false
	case q.resume > st.head:
		return protocol.ResumeResyncRequired, false
	case q.resume == st.head:
		return protocol.ResumeOK, false
	case st.oldest == 0 || q.resume+1 < st.oldest:
		return protocol.ResumeResyncRequired, false
	default:
		return protocol.ResumeOK, true
	}
}

// errReplayGap reports a hole in the events a replay was about to send.
var errReplayGap = errors.New("replay gap")

// retained reports whether every event in (after, upTo] is still in the log.
// Pruning removes only the oldest events, so a hole means an event was lost
// from the middle and no replay of that range can be complete.
func retained(ctx context.Context, db *sql.DB, after, upTo int64) (bool, error) {
	n, err := events.Count(ctx, db, after, upTo)
	if err != nil {
		return false, err
	}
	return n == upTo-after, nil
}

// replay writes the retained events in (after, upTo] that the viewer may
// see, judged by current visibility (a fresh membership snapshot), in seq
// order. Every event of the range must be present in sequence: a missing one
// yields errReplayGap instead of a partial replay.
func (c *conn) replay(ctx context.Context, v events.Viewer, after, upTo int64) error {
	db := c.g.o.DB.Read()
	m := events.NewMembershipCache(ctx, db, c.g.o.Logger)
	for after < upTo {
		batch, err := events.Range(ctx, db, after, upTo, replayBatch)
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			return errReplayGap
		}
		for _, e := range batch {
			if e.Seq != after+1 {
				return errReplayGap
			}
			after = e.Seq
			if !events.Visible(v, e, m) {
				continue
			}
			data, err := events.EncodeFrame(e)
			if err != nil {
				return err
			}
			if err := c.write(ctx, data); err != nil {
				return err
			}
		}
	}
	return nil
}
