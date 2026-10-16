// Package jobs is the control plane's work queue (docs/architecture.md §4.1):
// rows in the global jobs table, claimed with FOR UPDATE SKIP LOCKED so any
// number of `plumbline-server work` processes share it without running a job
// twice, retried with backoff, and pruned.
//
// A job is claimed in a short transaction (locked_at, attempts + 1) and run
// outside it, so a long job holds no row lock. A worker that dies mid-job
// leaves locked_at behind; after LockTimeout the job can be claimed again.
// A job that keeps failing ends as dead: done_at and last_error both set.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"runtime/debug"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/store"
)

// Kinds the jobs table accepts (its CHECK constraint).
const (
	KindEvaluate   = "evaluate"
	KindIssuePack  = "issue_pack"
	KindCheckpoint = "checkpoint"
	KindVantaSync  = "vanta_sync"
	KindPruneJobs  = "prune_jobs"
)

// Job is one claimed job.
type Job struct {
	ID       int64
	Kind     string
	TenantID *string
	Payload  json.RawMessage
	Attempt  int // 1 on the first run
}

// Handler runs one job. A returned error (or a panic) schedules a retry.
// Errors are stored in last_error: they must never contain secrets.
type Handler func(ctx context.Context, j Job) error

// Permanent marks an error that retrying cannot fix (bad input, nothing to
// do): the job is parked as dead at once instead of retried.
func Permanent(err error) error { return permanentError{err} }

type permanentError struct{ error }

func (p permanentError) Unwrap() error { return p.error }

// Execer is what Enqueue needs: a pool or a transaction, so a job can be
// enqueued atomically with the writes that call for it.
type Execer interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Enqueue adds a job to run at runAt (zero: now).
func Enqueue(ctx context.Context, q Execer, kind string, tenantID *string, payload any, runAt time.Time) (int64, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	var at *time.Time
	if !runAt.IsZero() {
		at = &runAt
	}
	var id int64
	err = q.QueryRow(ctx, `INSERT INTO jobs (kind, tenant_id, payload, run_at) VALUES ($1, $2, $3, coalesce($4, now())) RETURNING id`,
		kind, tenantID, string(b), at).Scan(&id)
	return id, err
}

// Worker claims and runs jobs of the kinds it has handlers for.
type Worker struct {
	Store       *store.Store
	Handlers    map[string]Handler
	ID          string // locked_by; default host:pid
	Log         *slog.Logger
	Poll        time.Duration                   // idle wait between claims; default 2 s (jittered)
	LockTimeout time.Duration                   // a lock older than this is a dead worker's; default 15 min
	MaxAttempts int                             // default 8
	Backoff     func(attempt int) time.Duration // default 30 s × 2^(attempt-1), capped at 1 h
}

func (w *Worker) defaults() {
	if w.ID == "" {
		host, _ := os.Hostname()
		w.ID = fmt.Sprintf("%s:%d", host, os.Getpid())
	}
	if w.Log == nil {
		w.Log = slog.New(slog.DiscardHandler)
	}
	if w.Poll <= 0 {
		w.Poll = 2 * time.Second
	}
	if w.LockTimeout <= 0 {
		w.LockTimeout = 15 * time.Minute
	}
	if w.MaxAttempts <= 0 {
		w.MaxAttempts = 8
	}
	if w.Backoff == nil {
		w.Backoff = func(attempt int) time.Duration {
			return min(30*time.Second<<min(attempt-1, 20), time.Hour)
		}
	}
}

func (w *Worker) kinds() []string {
	out := make([]string, 0, len(w.Handlers))
	for k := range w.Handlers {
		out = append(out, k)
	}
	return out
}

// claim locks the next due job of a handled kind, or returns nil.
func (w *Worker) claim(ctx context.Context) (*Job, error) {
	var j *Job
	err := w.Store.Public(ctx, func(tx pgx.Tx) error {
		var job Job
		var payload []byte
		err := tx.QueryRow(ctx, `
			SELECT id, kind, tenant_id::text, payload, attempts FROM jobs
			WHERE done_at IS NULL AND run_at <= now() AND kind = ANY($1)
			  AND (locked_at IS NULL OR locked_at < now() - make_interval(secs => $2))
			ORDER BY run_at, id
			FOR UPDATE SKIP LOCKED
			LIMIT 1`, w.kinds(), w.LockTimeout.Seconds()).Scan(&job.ID, &job.Kind, &job.TenantID, &payload, &job.Attempt)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		job.Attempt++
		job.Payload = payload
		if _, err := tx.Exec(ctx, `UPDATE jobs SET locked_at = now(), locked_by = $2, attempts = $3 WHERE id = $1`, job.ID, w.ID, job.Attempt); err != nil {
			return err
		}
		j = &job
		return nil
	})
	return j, err
}

// RunOnce claims and runs at most one job; it reports whether it found one.
func (w *Worker) RunOnce(ctx context.Context) (bool, error) {
	w.defaults()
	j, err := w.claim(ctx)
	if err != nil || j == nil {
		return false, err
	}
	err = w.run(ctx, *j)
	var q string
	var args []any
	switch {
	case err == nil:
		q, args = `UPDATE jobs SET done_at = now(), locked_at = NULL, locked_by = NULL, last_error = NULL WHERE id = $1`, []any{j.ID}
		w.Log.Info("job done", "job_id", j.ID, "kind", j.Kind, "attempt", j.Attempt)
	case j.Attempt >= w.MaxAttempts || errors.As(err, new(permanentError)):
		q, args = `UPDATE jobs SET done_at = now(), locked_at = NULL, locked_by = NULL, last_error = $2 WHERE id = $1`, []any{j.ID, truncate(err.Error())}
		w.Log.Error("job dead", "job_id", j.ID, "kind", j.Kind, "attempts", j.Attempt, "error", truncate(err.Error()))
	default:
		wait := w.Backoff(j.Attempt)
		q, args = `UPDATE jobs SET locked_at = NULL, locked_by = NULL, last_error = $2, run_at = now() + make_interval(secs => $3) WHERE id = $1`,
			[]any{j.ID, truncate(err.Error()), wait.Seconds()}
		w.Log.Warn("job failed; will retry", "job_id", j.ID, "kind", j.Kind, "attempt", j.Attempt, "retry_in", wait.String(), "error", truncate(err.Error()))
	}
	// Record the outcome even if ctx was cancelled while the job ran.
	if _, uerr := w.Store.Pool.Exec(context.WithoutCancel(ctx), q, args...); uerr != nil {
		return true, fmt.Errorf("jobs: recording job %d: %w", j.ID, uerr)
	}
	return true, nil
}

func (w *Worker) run(ctx context.Context, j Job) (err error) {
	defer func() {
		if r := recover(); r != nil {
			w.Log.Error("job panicked", "job_id", j.ID, "kind", j.Kind, "stack", string(debug.Stack()))
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return w.Handlers[j.Kind](ctx, j)
}

func truncate(s string) string {
	if len(s) > 500 {
		return s[:500]
	}
	return s
}

// Run works until ctx ends: jobs back to back while there are some, then a
// jittered poll. A database error backs off and retries.
func (w *Worker) Run(ctx context.Context) error {
	w.defaults()
	w.Log.Info("worker started", "worker", w.ID, "kinds", w.kinds())
	for {
		found, err := w.RunOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			var pe *pgconn.PgError
			if !errors.As(err, &pe) {
				w.Log.Error("worker: database error", "error", err.Error())
			}
		}
		if found && err == nil {
			continue
		}
		t := time.NewTimer(w.Poll/2 + rand.N(w.Poll))
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-t.C:
		}
	}
}

// EnsureScheduled enqueues a job of kind at runAt unless one is already
// pending; an advisory lock keeps two workers from both adding one.
func EnsureScheduled(ctx context.Context, s *store.Store, kind string, runAt time.Time) error {
	return ensure(ctx, s, kind, runAt, 0)
}

// ensure is EnsureScheduled, not counting job except (the one running now).
func ensure(ctx context.Context, s *store.Store, kind string, runAt time.Time, except int64) error {
	return s.Public(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('plumbline.jobs.' || $1))`, kind); err != nil {
			return err
		}
		var pending bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM jobs WHERE kind = $1 AND done_at IS NULL AND id <> $2)`, kind, except).Scan(&pending); err != nil {
			return err
		}
		if pending {
			return nil
		}
		_, err := Enqueue(ctx, tx, kind, nil, nil, runAt)
		return err
	})
}

// Prune is the prune_jobs handler: it deletes finished jobs older than
// retention (dead jobs too, after twice that) and schedules the next prune
// (once, even if this run is retried).
func Prune(s *store.Store, retention, every time.Duration) Handler {
	return func(ctx context.Context, j Job) error {
		if _, err := s.Pool.Exec(ctx, `DELETE FROM jobs WHERE done_at IS NOT NULL
			AND ((last_error IS NULL AND done_at < now() - make_interval(secs => $1)) OR done_at < now() - make_interval(secs => $2))`,
			retention.Seconds(), (2 * retention).Seconds()); err != nil {
			return err
		}
		return ensure(ctx, s, KindPruneJobs, time.Now().Add(every), j.ID)
	}
}
