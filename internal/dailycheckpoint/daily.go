// Package dailycheckpoint runs the checkpoint job (P4-7, D-043): shortly after
// each local midnight it signs a checkpoint over the previous day's pack
// digests, publishes it to the public repository, and stores it. Missed days
// are caught up in order, so the published log has no gaps.
package dailycheckpoint

import (
	"context"
	"crypto"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/lumetrix-advanced-technologies/tellproof/internal/checkpoint"
	"github.com/lumetrix-advanced-technologies/tellproof/internal/dsse"
	"github.com/lumetrix-advanced-technologies/tellproof/internal/jobs"
	"github.com/lumetrix-advanced-technologies/tellproof/internal/keys"
	"github.com/lumetrix-advanced-technologies/tellproof/internal/kmssign"
	"github.com/lumetrix-advanced-technologies/tellproof/internal/pack"
	"github.com/lumetrix-advanced-technologies/tellproof/internal/store"
)

// Publisher puts a checkpoint file in the public repository and returns the commit.
type Publisher interface {
	Publish(ctx context.Context, path string, content []byte, message string) (string, error)
}

// Job is the checkpoint job.
type Job struct {
	Store     *store.Store
	Signer    kmssign.Signer
	Publisher Publisher // nil: not published (git_commit stays empty)
	Timezone  string    // default Australia/Sydney
	RunAt     time.Duration
	Now       func() time.Time
	Log       *slog.Logger
}

func (j *Job) loc() *time.Location {
	tz := j.Timezone
	if tz == "" {
		tz = "Australia/Sydney"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.UTC
	}
	return loc
}

func (j *Job) now() time.Time {
	if j.Now != nil {
		return j.Now()
	}
	return time.Now()
}

// NextRun is the next local 00:10 (or RunAt past midnight) after now.
func (j *Job) NextRun() time.Time {
	after := j.RunAt
	if after == 0 {
		after = 10 * time.Minute
	}
	n := j.now().In(j.loc())
	y, m, d := n.Date()
	t := time.Date(y, m, d, 0, 0, 0, 0, j.loc()).Add(after)
	if !t.After(n) {
		t = time.Date(y, m, d+1, 0, 0, 0, 0, j.loc()).Add(after)
	}
	return t
}

// Handler checkpoints every day not yet checkpointed up to yesterday, then
// schedules the next run.
func (j *Job) Handler() jobs.Handler {
	return func(ctx context.Context, job jobs.Job) error {
		if err := j.CatchUp(ctx); err != nil {
			return err
		}
		return jobs.Reschedule(ctx, j.Store, job, j.NextRun())
	}
}

func day(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

// CatchUp checkpoints, in order, every local day after the last checkpoint
// (or from the first pack's day) up to yesterday. Days without packs get a
// checkpoint too, so the log is contiguous.
func (j *Job) CatchUp(ctx context.Context) error {
	loc := j.loc()
	yesterday := day(j.now().In(loc)).AddDate(0, 0, -1)
	var lastDay *time.Time
	var lastEnv []byte
	err := j.Store.Public(ctx, func(tx pgx.Tx) error {
		var d time.Time
		err := tx.QueryRow(ctx, `SELECT day, envelope::text FROM checkpoints ORDER BY day DESC LIMIT 1`).Scan(&d, &lastEnv)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		lastDay = &d
		return err
	})
	if err != nil {
		return err
	}
	var start time.Time
	var prev *dsse.Envelope
	if lastDay != nil {
		start = time.Date(lastDay.Year(), lastDay.Month(), lastDay.Day()+1, 0, 0, 0, 0, loc)
		if prev, err = dsse.Decode(lastEnv); err != nil {
			return fmt.Errorf("checkpoint: the last stored checkpoint does not decode: %w", err)
		}
	} else {
		var first *time.Time
		if err := j.Store.Public(ctx, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT first_pack_issued_at()`).Scan(&first)
		}); err != nil {
			return err
		}
		if first == nil {
			return nil // nothing issued yet: the log starts with the first pack's day
		}
		start = day(first.In(loc))
	}
	for d := start; !d.After(yesterday); d = d.AddDate(0, 0, 1) {
		env, err := j.checkpointDay(ctx, d, prev)
		if err != nil {
			return fmt.Errorf("checkpoint %s: %w", d.Format("2006-01-02"), err)
		}
		prev = env
	}
	return nil
}

func (j *Job) checkpointDay(ctx context.Context, d time.Time, prev *dsse.Envelope) (*dsse.Envelope, error) {
	from, to := d, d.AddDate(0, 0, 1)
	var digests []string
	if err := j.Store.Public(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT digest FROM checkpoint_digests($1, $2)`, from, to)
		if err != nil {
			return err
		}
		digests, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	}); err != nil {
		return nil, err
	}
	var prevDigest *string
	if prev != nil {
		pd := checkpoint.Digest(prev)
		prevDigest = &pd
	}
	issued := j.now().In(j.loc()).Truncate(time.Second)
	pred, err := checkpoint.Build(d.Format("2006-01-02"), j.loc().String(), issued, digests, prevDigest)
	if err != nil {
		return nil, err
	}
	payload, err := checkpoint.Statement(pred)
	if err != nil {
		return nil, err
	}
	if err := j.Store.Public(ctx, func(tx pgx.Tx) error {
		return keys.CheckSigner(ctx, tx, j.Signer.KeyID(), j.Signer.PublicKey(), issued)
	}); err != nil {
		return nil, err
	}
	env, err := dsse.Sign(ctx, j.Signer, dsse.PayloadTypeInToto, payload)
	if err != nil {
		return nil, err
	}
	// Verify before publishing: what goes public must verify, and follow yesterday's.
	if _, err := checkpoint.Verify(env, pack.KeySetOf(map[string]crypto.PublicKey{j.Signer.KeyID(): j.Signer.PublicKey()}), prev); err != nil {
		return nil, fmt.Errorf("the new checkpoint does not verify: %w", err)
	}
	raw, err := env.Encode()
	if err != nil {
		return nil, err
	}
	var commit *string
	if j.Publisher != nil {
		file, _ := json.MarshalIndent(json.RawMessage(raw), "", "  ")
		sha, err := j.Publisher.Publish(ctx, "checkpoints/"+d.Format("2006/01/02")+".json", append(file, '\n'),
			fmt.Sprintf("checkpoint %s: %d pack(s), root %s", pred.Day, pred.PackCount, pred.Root))
		if err != nil {
			return nil, err
		}
		commit = &sha
	}
	if err := j.Store.Public(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO checkpoints (day, root, pack_count, envelope, git_commit) VALUES ($1, $2, $3, $4, $5)`,
			pred.Day, pred.Root, pred.PackCount, string(raw), commit)
		return err
	}); err != nil {
		return nil, err
	}
	if j.Log != nil {
		j.Log.Info("checkpoint stored", "day", pred.Day, "packs", pred.PackCount, "root", pred.Root, "digest", checkpoint.Digest(env), "published", commit != nil)
	}
	return env, nil
}
