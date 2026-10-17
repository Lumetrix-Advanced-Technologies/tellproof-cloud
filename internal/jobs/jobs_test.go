package jobs_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/lumetrix-advanced-technologies/witnessmark/internal/jobs"
	"github.com/lumetrix-advanced-technologies/witnessmark/internal/store"
	"github.com/lumetrix-advanced-technologies/witnessmark/internal/store/storetest"
)

type db struct {
	app   *store.Store
	owner *pgx.Conn
}

func setup(t *testing.T) db {
	t.Helper()
	url := storetest.MigratedDB(t)
	owner, err := pgx.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { owner.Close(context.Background()) })
	return db{app: storetest.App(t, url), owner: owner}
}

func (d db) enqueue(t *testing.T, kind string, payload any) int64 {
	t.Helper()
	id, err := jobs.Enqueue(context.Background(), d.app.Pool, kind, nil, payload, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (d db) row(t *testing.T, id int64) (attempts int, done bool, lastError *string) {
	t.Helper()
	if err := d.owner.QueryRow(context.Background(), `SELECT attempts, done_at IS NOT NULL, last_error FROM jobs WHERE id = $1`, id).
		Scan(&attempts, &done, &lastError); err != nil {
		t.Fatal(err)
	}
	return
}

func noBackoff(int) time.Duration { return 0 }

func TestRunsAJobOnce(t *testing.T) {
	d := setup(t)
	id := d.enqueue(t, jobs.KindEvaluate, map[string]string{"period": "2026-09"})
	var got []jobs.Job
	w := &jobs.Worker{Store: d.app, Handlers: map[string]jobs.Handler{jobs.KindEvaluate: func(_ context.Context, j jobs.Job) error {
		got = append(got, j)
		return nil
	}}}
	ctx := context.Background()
	if found, err := w.RunOnce(ctx); !found || err != nil {
		t.Fatalf("RunOnce: %v %v", found, err)
	}
	if found, err := w.RunOnce(ctx); found || err != nil {
		t.Errorf("a second RunOnce found %v (%v)", found, err)
	}
	if len(got) != 1 || got[0].ID != id || got[0].Attempt != 1 || string(got[0].Payload) != `{"period": "2026-09"}` {
		t.Errorf("handler saw %+v", got)
	}
	if attempts, done, lastErr := d.row(t, id); attempts != 1 || !done || lastErr != nil {
		t.Errorf("row: attempts %d done %v last_error %v", attempts, done, lastErr)
	}
}

// Many workers share the queue without running any job twice.
func TestConcurrentWorkersRunEachJobOnce(t *testing.T) {
	d := setup(t)
	const n = 60
	for i := range n {
		d.enqueue(t, jobs.KindEvaluate, map[string]int{"i": i})
	}
	var mu sync.Mutex
	runs := map[int64]int{}
	var slow atomic.Int32
	handler := func(_ context.Context, j jobs.Job) error {
		if slow.Add(1)%7 == 0 {
			time.Sleep(20 * time.Millisecond) // jobs run outside the claim: others keep claiming
		}
		mu.Lock()
		runs[j.ID]++
		mu.Unlock()
		return nil
	}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := &jobs.Worker{Store: d.app, ID: string(rune('a' + i)), Handlers: map[string]jobs.Handler{jobs.KindEvaluate: handler}}
			for {
				found, err := w.RunOnce(context.Background())
				if err != nil {
					t.Error(err)
					return
				}
				if !found {
					return
				}
			}
		}()
	}
	wg.Wait()
	if len(runs) != n {
		t.Errorf("%d of %d jobs ran", len(runs), n)
	}
	for id, c := range runs {
		if c != 1 {
			t.Errorf("job %d ran %d times", id, c)
		}
	}
}

func TestRetriesThenSucceeds(t *testing.T) {
	d := setup(t)
	id := d.enqueue(t, jobs.KindIssuePack, nil)
	calls := 0
	w := &jobs.Worker{Store: d.app, Backoff: noBackoff, Handlers: map[string]jobs.Handler{jobs.KindIssuePack: func(_ context.Context, j jobs.Job) error {
		calls++
		if j.Attempt != calls {
			t.Errorf("attempt %d on call %d", j.Attempt, calls)
		}
		if calls < 3 {
			return errors.New("kms unavailable")
		}
		return nil
	}}}
	for range 5 {
		_, _ = w.RunOnce(context.Background())
	}
	if attempts, done, lastErr := d.row(t, id); calls != 3 || attempts != 3 || !done || lastErr != nil {
		t.Errorf("calls %d, attempts %d, done %v, last_error %v", calls, attempts, done, lastErr)
	}
}

func TestBackoffDelaysTheRetry(t *testing.T) {
	d := setup(t)
	id := d.enqueue(t, jobs.KindIssuePack, nil)
	w := &jobs.Worker{Store: d.app, Handlers: map[string]jobs.Handler{jobs.KindIssuePack: func(context.Context, jobs.Job) error {
		return errors.New("no")
	}}}
	_, _ = w.RunOnce(context.Background()) // default backoff: 30 s
	var wait float64
	_ = d.owner.QueryRow(context.Background(), `SELECT extract(epoch FROM run_at - now()) FROM jobs WHERE id = $1`, id).Scan(&wait)
	if wait < 25 || wait > 31 {
		t.Errorf("retry in %.1f s, want about 30", wait)
	}
	if found, _ := w.RunOnce(context.Background()); found {
		t.Error("a job waiting for its retry was claimed")
	}
}

func TestGivesUpAfterMaxAttempts(t *testing.T) {
	d := setup(t)
	id := d.enqueue(t, jobs.KindCheckpoint, nil)
	calls := 0
	w := &jobs.Worker{Store: d.app, Backoff: noBackoff, MaxAttempts: 3, Handlers: map[string]jobs.Handler{jobs.KindCheckpoint: func(context.Context, jobs.Job) error {
		calls++
		panic("bug") // a panic is a failure, not a crash
	}}}
	for range 6 {
		if _, err := w.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	attempts, done, lastErr := d.row(t, id)
	if calls != 3 || attempts != 3 || !done || lastErr == nil || *lastErr != "panic: bug" {
		t.Errorf("calls %d, attempts %d, done %v, last_error %v", calls, attempts, done, lastErr)
	}
}

func TestDeadWorkersLockExpires(t *testing.T) {
	d := setup(t)
	id := d.enqueue(t, jobs.KindEvaluate, nil)
	ran := 0
	w := &jobs.Worker{Store: d.app, Handlers: map[string]jobs.Handler{jobs.KindEvaluate: func(context.Context, jobs.Job) error { ran++; return nil }}}
	// A worker claimed it and died: locked, never finished.
	if _, err := d.owner.Exec(context.Background(), `UPDATE jobs SET locked_at = now(), locked_by = 'dead', attempts = 1 WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if found, _ := w.RunOnce(context.Background()); found {
		t.Fatal("a freshly locked job was claimed")
	}
	_, _ = d.owner.Exec(context.Background(), `UPDATE jobs SET locked_at = now() - interval '16 minutes' WHERE id = $1`, id)
	if found, _ := w.RunOnce(context.Background()); !found || ran != 1 {
		t.Errorf("after the lock expired: found %v, ran %d", found, ran)
	}
	if attempts, done, _ := d.row(t, id); attempts != 2 || !done {
		t.Errorf("attempts %d done %v", attempts, done)
	}
}

func TestClaimsOnlyDueJobsOfItsKinds(t *testing.T) {
	d := setup(t)
	_, _ = jobs.Enqueue(context.Background(), d.app.Pool, jobs.KindEvaluate, nil, nil, time.Now().Add(time.Hour))
	d.enqueue(t, jobs.KindVantaSync, nil)
	w := &jobs.Worker{Store: d.app, Handlers: map[string]jobs.Handler{jobs.KindEvaluate: func(context.Context, jobs.Job) error { return nil }}}
	if found, err := w.RunOnce(context.Background()); found || err != nil {
		t.Errorf("claimed a future job or another kind: %v %v", found, err)
	}
}

func TestEnqueueIsAtomicWithTheCaller(t *testing.T) {
	d := setup(t)
	ctx := context.Background()
	_ = d.app.Public(ctx, func(tx pgx.Tx) error {
		if _, err := jobs.Enqueue(ctx, tx, jobs.KindEvaluate, nil, nil, time.Time{}); err != nil {
			t.Fatal(err)
		}
		return errors.New("rolled back")
	})
	var n int
	_ = d.owner.QueryRow(ctx, `SELECT count(*) FROM jobs`).Scan(&n)
	if n != 0 {
		t.Errorf("%d jobs after a rolled-back enqueue", n)
	}
}

func TestPrune(t *testing.T) {
	d := setup(t)
	ctx := context.Background()
	old := d.enqueue(t, jobs.KindEvaluate, nil)
	recent := d.enqueue(t, jobs.KindEvaluate, nil)
	deadOld := d.enqueue(t, jobs.KindEvaluate, nil)
	deadRecent := d.enqueue(t, jobs.KindEvaluate, nil)
	pending := d.enqueue(t, jobs.KindEvaluate, nil)
	for _, q := range []struct {
		id  int64
		sql string
	}{
		{old, `UPDATE jobs SET done_at = now() - interval '40 days' WHERE id = $1`},
		{recent, `UPDATE jobs SET done_at = now() - interval '1 day' WHERE id = $1`},
		{deadOld, `UPDATE jobs SET done_at = now() - interval '70 days', last_error = 'x' WHERE id = $1`},
		{deadRecent, `UPDATE jobs SET done_at = now() - interval '40 days', last_error = 'x' WHERE id = $1`},
	} {
		if _, err := d.owner.Exec(ctx, q.sql, q.id); err != nil {
			t.Fatal(err)
		}
	}
	if err := jobs.EnsureScheduled(ctx, d.app, jobs.KindPruneJobs, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := jobs.EnsureScheduled(ctx, d.app, jobs.KindPruneJobs, time.Time{}); err != nil { // already pending: no second one
		t.Fatal(err)
	}
	w := &jobs.Worker{Store: d.app, Handlers: map[string]jobs.Handler{jobs.KindPruneJobs: jobs.Prune(d.app, 30*24*time.Hour, 24*time.Hour)}}
	if found, err := w.RunOnce(ctx); !found || err != nil {
		t.Fatalf("prune: %v %v", found, err)
	}
	rows, _ := d.owner.Query(ctx, `SELECT id FROM jobs WHERE kind = 'evaluate' ORDER BY id`)
	left, _ := pgx.CollectRows(rows, pgx.RowTo[int64])
	if want := []int64{recent, deadRecent, pending}; len(left) != 3 || left[0] != want[0] || left[1] != want[1] || left[2] != want[2] {
		t.Errorf("after pruning: %v, want %v", left, want)
	}
	var prunes, due int
	_ = d.owner.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE run_at > now() + interval '23 hours') FROM jobs WHERE kind = 'prune_jobs' AND done_at IS NULL`).Scan(&prunes, &due)
	if prunes != 1 || due != 1 {
		t.Errorf("next prune: %d pending, %d a day out", prunes, due)
	}
}

func TestRunStopsWithItsContext(t *testing.T) {
	d := setup(t)
	d.enqueue(t, jobs.KindEvaluate, nil)
	ran := make(chan struct{}, 1)
	w := &jobs.Worker{Store: d.app, Poll: 10 * time.Millisecond, Handlers: map[string]jobs.Handler{jobs.KindEvaluate: func(context.Context, jobs.Job) error {
		ran <- struct{}{}
		return nil
	}}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- w.Run(ctx) }()
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not run the job")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestPermanentErrorsAreNotRetried(t *testing.T) {
	d := setup(t)
	id := d.enqueue(t, jobs.KindIssuePack, nil)
	calls := 0
	w := &jobs.Worker{Store: d.app, Backoff: noBackoff, Handlers: map[string]jobs.Handler{jobs.KindIssuePack: func(context.Context, jobs.Job) error {
		calls++
		return jobs.Permanent(errors.New("no evidence in this period"))
	}}}
	for range 3 {
		_, _ = w.RunOnce(context.Background())
	}
	attempts, done, lastErr := d.row(t, id)
	if calls != 1 || attempts != 1 || !done || lastErr == nil || *lastErr != "no evidence in this period" {
		t.Errorf("calls %d, attempts %d, done %v, last_error %v", calls, attempts, done, lastErr)
	}
}
