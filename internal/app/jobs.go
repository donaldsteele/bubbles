package app

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/donaldsteele/bubbles/internal/audit"
	"github.com/donaldsteele/bubbles/internal/events"
	"github.com/donaldsteele/bubbles/internal/reset"
	"github.com/donaldsteele/bubbles/internal/retention"
)

// Job intervals (pipeline step 18).
const (
	SANWatchInterval   = 5 * time.Minute
	RenewCheckInterval = 12 * time.Hour
	// SessionPurgeInterval is how often expired sessions and old password
	// reset rows are deleted.
	SessionPurgeInterval = time.Hour
)

// Ticker is the part of time.Ticker the job runner uses; tests inject fakes.
type Ticker interface {
	C() <-chan time.Time
	Stop()
}

type realTicker struct{ t *time.Ticker }

func newRealTicker(d time.Duration) Ticker { return realTicker{time.NewTicker(d)} }
func (r realTicker) C() <-chan time.Time   { return r.t.C }
func (r realTicker) Stop()                 { r.t.Stop() }

// Job is a periodic background task.
type Job struct {
	Name     string
	Interval time.Duration
	Run      func(context.Context) error
}

// defaultJobs is the job registry. Later milestones add pruning, retention
// and cleanup jobs here.
func (a *App) defaultJobs() []Job {
	return []Job{
		// Re-issues the leaf when the LAN addresses (DHCP, new NIC) or
		// host name change; reloads bring-your-own files when they change.
		{Name: "tls-san-watch", Interval: SANWatchInterval, Run: a.checkCert},
		// Renews the leaf when fewer than 30 days remain.
		{Name: "tls-renewal", Interval: RenewCheckInterval, Run: a.checkCert},
		// Deletes events older than the 7-day resume window.
		{Name: "events-prune", Interval: events.PruneInterval, Run: a.pruneEvents},
		// Moves presence online -> idle -> offline (5 min activity, 30 s grace).
		{Name: "presence-sweep", Interval: events.PresenceSweepInterval, Run: a.sweepPresence},
		// Deletes expired sessions and long-expired password-reset rows.
		{Name: "session-purge", Interval: SessionPurgeInterval, Run: a.purgeSessions},
		// Hard-deletes messages past their channel's retention, uploads that
		// were never attached, unreferenced blobs and stale spool files.
		{Name: "retention", Interval: retention.Interval, Run: a.retain.Run},
		// Warns once when only the host itself has connected since setup.
		{Name: "isolation-hint", Interval: time.Minute, Run: a.checkClientIsolation},
	}
}

func (a *App) checkCert(context.Context) error {
	_, err := a.Certs().Check()
	return err
}

func (a *App) pruneEvents(ctx context.Context) error {
	_, err := events.Prune(ctx, a.db, a.opts.Now())
	return err
}

func (a *App) sweepPresence(context.Context) error {
	a.hub.SweepPresence()
	return nil
}

func (a *App) purgeSessions(ctx context.Context) error {
	_, err := a.auth.PurgeExpired(ctx)
	return err
}

// startJobs runs each job on its own ticker until ctx is done. Tickers are
// created before startJobs returns. A failing run is logged and retried at
// the next tick.
func startJobs(ctx context.Context, wg *sync.WaitGroup, jobs []Job, newTicker func(time.Duration) Ticker, logger *slog.Logger) {
	for _, job := range jobs {
		t := newTicker(job.Interval)
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C():
					if err := job.Run(ctx); err != nil {
						logger.Warn("job failed", "job", job.Name, "err", err)
					}
				}
			}
		}()
	}
}

// startBackground starts the jobs, mDNS and a trash sweep. The caller holds
// lifeMu.
func (a *App) startBackground() {
	ctx, cancel := context.WithCancel(context.Background())
	a.bgCancel = cancel
	startJobs(ctx, &a.jobsWG, a.jobs, a.opts.NewTicker, a.logger)
	a.startMDNS(ctx)
	a.sweepTrash()
}

// stopBackground stops what startBackground started and waits for it. The
// caller holds lifeMu.
func (a *App) stopBackground() {
	if a.bgCancel == nil {
		return
	}
	a.bgCancel()
	a.bgCancel = nil
	a.jobsWG.Wait()
	a.stopMDNS()
}

// sweepTrash deletes the files directories moved aside by resets, in the
// background (pipeline 18: at startup and after every reset).
func (a *App) sweepTrash() {
	a.trashWG.Add(1)
	go func() {
		defer a.trashWG.Done()
		n, err := reset.SweepTrash(a.life, a.cfg.DataDir)
		if err != nil && a.life.Err() == nil {
			a.logger.Warn("sweep reset trash", "err", err)
		}
		if n > 0 {
			a.logger.Info("deleted the files of a reset", "dirs", n)
		}
	}()
}

// isolationHintDelay is how long after setup a missing non-loopback client
// is tolerated before the console is told about Wi-Fi client isolation.
const isolationHintDelay = 10 * time.Minute

// checkClientIsolation logs one WARN when no client other than the host
// itself has connected isolationHintDelay after setup completed (the same
// signal as the admin overview's client_isolation_hint). It stops checking
// once it has either warned or seen a remote client.
func (a *App) checkClientIsolation(ctx context.Context) error {
	if a.isolationDone.Load() {
		return nil
	}
	at, ok, err := audit.SetupTime(ctx, a.db)
	if err != nil || !ok {
		return err
	}
	if a.opts.Now().Sub(time.UnixMilli(at)) < isolationHintDelay {
		return nil
	}
	remote, err := audit.HasRemoteActor(ctx, a.db, audit.ActionSetup, audit.ActionLoginSuccess, audit.ActionRegister, audit.ActionInviteAccept)
	if err != nil {
		return err
	}
	a.isolationDone.Store(true)
	if !remote {
		a.logger.Warn("no other device has connected since setup; if phones cannot reach this address, " +
			"the Wi-Fi router may have client isolation (AP isolation) enabled")
	}
	return nil
}
