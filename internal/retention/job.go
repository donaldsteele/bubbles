// Package retention is the hourly clean-up job: it hard-deletes messages older
// than the retention of their channel, deletes uploads that were never
// attached, removes blobs that no file refers to any more, and once a day
// sweeps the blob store for blobs that no row refers to at all.
package retention

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/donaldsteele/bubbles/internal/settings"
	"github.com/donaldsteele/bubbles/internal/store"
)

// Job parameters.
const (
	// Interval is how often the app runs the job.
	Interval = time.Hour
	// BatchSize is the number of messages or files deleted per transaction,
	// so the single writer is never held for long.
	BatchSize = 500
	// OrphanAge is how long an upload may stay unattached before it is
	// deleted.
	OrphanAge = 24 * time.Hour
	// OrphanSweepInterval is how often RunOnce walks the blob store for blobs
	// that no files row refers to.
	OrphanSweepInterval = 24 * time.Hour
	// OrphanBlobGrace is how recently a blob may have been written and still be
	// spared by that walk: an upload writes its blob before its row.
	OrphanBlobGrace = time.Hour
)

// Blobs is the file store the job cleans up after.
type Blobs interface {
	// RemoveUnreferenced deletes the blobs of the given SHA-256 values that no
	// files row refers to, and returns how many it removed.
	RemoveUnreferenced(ctx context.Context, shas []string) (int, error)
	// SweepTmp deletes upload spool files older than maxAge.
	SweepTmp(maxAge time.Duration) (int, error)
	// SweepOrphanBlobs deletes the blobs no files row refers to, sparing those
	// modified within minAge, and returns how many it removed.
	SweepOrphanBlobs(ctx context.Context, minAge time.Duration) (int, error)
}

// Options configure a Job.
type Options struct {
	// DB is the store; required.
	DB *store.DB
	// Settings supplies default_retention_days; required.
	Settings *settings.Store
	// Blobs removes blobs from disk; required.
	Blobs Blobs
	// Now is the clock; nil means time.Now. It should be the clock the store
	// uses.
	Now func() time.Time
	// Logger receives a summary of what a run deleted; nil discards it.
	Logger *slog.Logger
}

// Job is the retention and file clean-up job.
type Job struct {
	db       *store.DB
	settings *settings.Store
	blobs    Blobs
	now      func() time.Time
	log      *slog.Logger

	// mu guards lastOrphanSweep, the time the orphan blob sweep last ran.
	mu              sync.Mutex
	lastOrphanSweep time.Time
}

// New returns a Job.
func New(o Options) *Job {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	return &Job{db: o.DB, settings: o.Settings, blobs: o.Blobs, now: o.Now, log: o.Logger}
}

// Result counts what one run deleted.
type Result struct {
	// Messages are the expired messages hard-deleted.
	Messages int
	// Files are the unattached uploads deleted.
	Files int
	// Blobs are the blobs removed from disk.
	Blobs int
	// TmpFiles are the stale upload spool files removed.
	TmpFiles int
	// OrphanBlobs are the blobs found by the daily sweep that no files row
	// refers to, removed from disk. It is zero on the runs the sweep is not due.
	OrphanBlobs int
}

// Run is the job function the app registers: RunOnce, with a log line when
// anything was deleted.
func (j *Job) Run(ctx context.Context) error {
	res, err := j.RunOnce(ctx)
	if res != (Result{}) {
		j.log.Info("retention run", "messages", res.Messages, "files", res.Files, "blobs", res.Blobs,
			"tmp_files", res.TmpFiles, "orphan_blobs", res.OrphanBlobs)
	}
	return err
}

// RunOnce deletes expired messages, then orphaned uploads, then the blobs that
// lost their last file row, then stale spool files, then, once a day (the
// first run included, so it happens at startup), the blobs no row refers to.
// A failing step does not stop the later ones; the errors are joined.
func (j *Job) RunOnce(ctx context.Context) (Result, error) {
	var res Result
	var errs []error
	n, err := j.deleteExpiredMessages(ctx)
	res.Messages = n
	if err != nil {
		errs = append(errs, err)
	}
	files, blobs, err := j.deleteOrphanFiles(ctx)
	res.Files, res.Blobs = files, blobs
	if err != nil {
		errs = append(errs, err)
	}
	tmp, err := j.blobs.SweepTmp(OrphanAge)
	res.TmpFiles = tmp
	if err != nil {
		errs = append(errs, fmt.Errorf("sweep tmp: %w", err))
	}
	if j.claimOrphanSweep() {
		orphans, err := j.blobs.SweepOrphanBlobs(ctx, OrphanBlobGrace)
		res.OrphanBlobs = orphans
		if err != nil {
			errs = append(errs, fmt.Errorf("sweep orphan blobs: %w", err))
		}
	}
	return res, errors.Join(errs...)
}

// claimOrphanSweep reports whether the orphan blob sweep is due and, if so,
// records this run as its last. The time is recorded before the sweep runs, so
// a failing sweep is retried on the next day, not on every hour.
func (j *Job) claimOrphanSweep() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	now := j.now()
	if !j.lastOrphanSweep.IsZero() && now.Sub(j.lastOrphanSweep) < OrphanSweepInterval {
		return false
	}
	j.lastOrphanSweep = now
	return true
}

// channelRetention is a channel with the retention that applies to it.
type channelRetention struct {
	id   string
	days int
}

// effectiveRetention lists the channels that expire messages: the channel's
// own retention_days, else default_retention_days; channels with neither keep
// everything.
func (j *Job) effectiveRetention(ctx context.Context) ([]channelRetention, error) {
	def := j.settings.DefaultRetentionDays()
	rows, err := j.db.Read().QueryContext(ctx, `SELECT id, retention_days FROM channels`)
	if err != nil {
		return nil, fmt.Errorf("list channels: %w", err)
	}
	defer rows.Close()
	var out []channelRetention
	for rows.Next() {
		var (
			id   string
			days *int
		)
		if err := rows.Scan(&id, &days); err != nil {
			return nil, fmt.Errorf("list channels: %w", err)
		}
		if days == nil {
			days = def
		}
		if days != nil {
			out = append(out, channelRetention{id: id, days: *days})
		}
	}
	return out, rows.Err()
}

// deleteExpiredMessages hard-deletes, channel by channel, the messages older
// than the channel's retention, BatchSize per transaction.
func (j *Job) deleteExpiredMessages(ctx context.Context) (int, error) {
	channels, err := j.effectiveRetention(ctx)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, ch := range channels {
		cutoff := j.now().Add(-time.Duration(ch.days) * 24 * time.Hour).UnixMilli()
		for {
			if err := ctx.Err(); err != nil {
				return total, err
			}
			n, err := j.deleteMessageBatch(ctx, ch.id, cutoff)
			total += n
			if err != nil {
				return total, err
			}
			if n == 0 {
				break
			}
		}
	}
	return total, nil
}

// deleteMessageBatch deletes up to BatchSize expired messages of one channel
// in one transaction and returns how many. A root message with any reply left
// is kept until its last reply expired and went first, so a thread is never
// cut in the middle; roots that survive get their reply count refreshed. The
// rows that depend on a message (attachments, reactions, mentions, pins,
// alerts, the search index) go with it through the schema's cascades, and
// the events that name it are deleted so the event log keeps no copy of it.
//
// Retention hard-deletes emit no WS events: nothing is published to the hub for
// them, so the event log is cleaned here and nowhere else. The events scan is
// not skipped for cutoffs older than the event retention (events.Retention,
// seven days): message.updated, reaction, pin and alert events of an old
// message can be younger than that, and they would keep a copy of the message.
func (j *Job) deleteMessageBatch(ctx context.Context, channelID string, cutoff int64) (int, error) {
	var n int
	err := j.db.Do(ctx, func(tx *store.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id, thread_root_id FROM messages m
			WHERE m.channel_id = ? AND m.created_at < ?
			AND NOT EXISTS (SELECT 1 FROM messages r WHERE r.thread_root_id = m.id)
			LIMIT ?`, channelID, cutoff, BatchSize)
		if err != nil {
			return fmt.Errorf("select expired messages: %w", err)
		}
		var ids []string
		roots := map[string]bool{}
		for rows.Next() {
			var id string
			var root *string
			if err := rows.Scan(&id, &root); err != nil {
				rows.Close()
				return fmt.Errorf("select expired messages: %w", err)
			}
			ids = append(ids, id)
			if root != nil {
				roots[*root] = true
			}
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return fmt.Errorf("select expired messages: %w", err)
		}
		if len(ids) == 0 {
			return nil
		}
		args := make([]any, len(ids))
		for i, id := range ids {
			args[i] = id
		}
		in := placeholders(len(ids))
		if _, err := tx.Exec(`DELETE FROM events WHERE
			(type IN ('message.created', 'message.updated', 'message.deleted') AND json_extract(payload, '$.id') IN (`+in+`))
			OR (type IN ('reaction.added', 'reaction.removed', 'pin.added', 'pin.removed', 'alert.updated')
				AND json_extract(payload, '$.message_id') IN (`+in+`))`, append(args, args...)...); err != nil {
			return fmt.Errorf("delete message events: %w", err)
		}
		if _, err := tx.Exec(`DELETE FROM messages WHERE id IN (`+in+`)`, args...); err != nil {
			return fmt.Errorf("delete expired messages: %w", err)
		}
		for root := range roots {
			_, err := tx.Exec(`UPDATE messages SET
					reply_count = (SELECT COUNT(*) FROM messages r WHERE r.thread_root_id = messages.id AND r.deleted_at IS NULL),
					last_reply_at = (SELECT MAX(r.created_at) FROM messages r WHERE r.thread_root_id = messages.id AND r.deleted_at IS NULL)
				WHERE id = ?`, root)
			if err != nil {
				return fmt.Errorf("refresh thread: %w", err)
			}
		}
		n = len(ids)
		return nil
	})
	return n, err
}

// deleteOrphanFiles deletes the files rows uploaded more than OrphanAge ago
// that no message attaches and no user wears as an avatar, BatchSize per
// transaction, then removes the blobs that no row refers to any more. It
// returns the rows deleted and the blobs removed.
func (j *Job) deleteOrphanFiles(ctx context.Context) (files, blobs int, err error) {
	cutoff := j.now().Add(-OrphanAge).UnixMilli()
	seen := map[string]bool{}
	var shas []string
	for {
		if err := ctx.Err(); err != nil {
			return files, blobs, err
		}
		var batchSHAs []string
		err := j.db.Do(ctx, func(tx *store.Tx) error {
			rows, err := tx.QueryContext(ctx, `SELECT f.id, f.sha256 FROM files f
				WHERE f.created_at < ?
				AND NOT EXISTS (SELECT 1 FROM attachments a WHERE a.file_id = f.id)
				AND NOT EXISTS (SELECT 1 FROM users u WHERE u.avatar_file_id = f.id)
				LIMIT ?`, cutoff, BatchSize)
			if err != nil {
				return fmt.Errorf("select orphan files: %w", err)
			}
			var ids []string
			for rows.Next() {
				var id, sha string
				if err := rows.Scan(&id, &sha); err != nil {
					rows.Close()
					return fmt.Errorf("select orphan files: %w", err)
				}
				ids = append(ids, id)
				batchSHAs = append(batchSHAs, sha)
			}
			if err := errors.Join(rows.Err(), rows.Close()); err != nil {
				return fmt.Errorf("select orphan files: %w", err)
			}
			if len(ids) == 0 {
				return nil
			}
			args := make([]any, len(ids))
			for i, id := range ids {
				args[i] = id
			}
			if _, err := tx.Exec(`DELETE FROM files WHERE id IN (`+placeholders(len(ids))+`)`, args...); err != nil {
				return fmt.Errorf("delete orphan files: %w", err)
			}
			return nil
		})
		if err != nil {
			return files, blobs, err
		}
		if len(batchSHAs) == 0 {
			break
		}
		files += len(batchSHAs)
		for _, sha := range batchSHAs {
			if !seen[sha] {
				seen[sha] = true
				shas = append(shas, sha)
			}
		}
	}
	// The blobs go once the transactions committed, and only if no other row
	// (an identical upload by someone else) still refers to them.
	removed, err := j.blobs.RemoveUnreferenced(ctx, shas)
	return files, removed, err
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
