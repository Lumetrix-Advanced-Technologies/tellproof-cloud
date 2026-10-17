// Package issue builds, signs and stores evidence packs (docs/architecture.md
// §4.4). One issue, in one tenant-scoped transaction holding the system's row
// lock, so concurrent requests for a system form one unbroken chain:
//
//  1. load the system's declarations and the period's batches and registers;
//  2. evaluate the eleven objectives and store the evaluation;
//  3. build the predicate: chain fields from the system's latest pack, the
//     period root over the batches' window roots (D-027), batch digests;
//  4. JCS the statement, sign it (KMS by digest, D-006) and verify it
//     locally, with the previous pack, before anything is stored;
//  5. insert the pack.
package issue

import (
	"context"
	"crypto"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/lumetrix-advanced-technologies/tellproof/internal/batch"
	"github.com/lumetrix-advanced-technologies/tellproof/internal/dsse"
	"github.com/lumetrix-advanced-technologies/tellproof/internal/evaluate"
	"github.com/lumetrix-advanced-technologies/tellproof/internal/jcs"
	"github.com/lumetrix-advanced-technologies/tellproof/internal/jobs"
	"github.com/lumetrix-advanced-technologies/tellproof/internal/keys"
	"github.com/lumetrix-advanced-technologies/tellproof/internal/kmssign"
	"github.com/lumetrix-advanced-technologies/tellproof/internal/merkle"
	"github.com/lumetrix-advanced-technologies/tellproof/internal/pack"
	"github.com/lumetrix-advanced-technologies/tellproof/internal/store"
	specdata "github.com/lumetrix-advanced-technologies/tellproof/spec"
)

// Leaf and node rules as written into production packs (D-027).
const (
	LeafRule = "sha256(0x00 || salt16 || trace_id), committed per hourly window by the evidence agent"
	NodeRule = "sha256(0x01 || left || right); odd node carried up; the period root is over the window roots in time order"
)

// Assurance level 1 (docs/project-overview.md): provider usage comes from the vendor's agent.
var assurance = pack.AssuranceLevel{Level: 1,
	Meaning: "Telemetry and provider usage collected by the Tellproof evidence agent in the vendor's environment and signed per hour; reconciliation computed by the issuer."}

var errNoSystem = errors.New("issue: no AI system in this tenant")

// ErrNoEvidence: the period has no committed traces, so there is nothing to attest.
var ErrNoEvidence = errors.New("issue: no committed traces in this period")

// Request asks for a pack over whole local days (YYYY-MM-DD, inclusive).
type Request struct {
	TenantID    string
	SystemID    string
	PeriodStart string
	PeriodEnd   string
}

// Issued is a stored pack.
type Issued struct {
	PackID       string
	SeqNo        int
	Digest       string
	EvaluationID string
	Envelope     []byte
}

// Issuer issues packs.
type Issuer struct {
	Store  *store.Store
	Signer kmssign.Signer
	Now    func() time.Time
	Log    *slog.Logger
}

type systemRow struct {
	sys        evaluate.System
	packPrefix string
}

// Issue builds, signs, verifies and stores the next pack for r.SystemID.
func (is *Issuer) Issue(ctx context.Context, r Request) (*Issued, error) {
	now := time.Now
	if is.Now != nil {
		now = is.Now
	}
	var out *Issued
	err := is.Store.WithTenant(ctx, r.TenantID, func(tx pgx.Tx) error {
		s, err := loadSystem(ctx, tx, r)
		if err != nil {
			return err
		}
		start, end, err := evaluate.PeriodBounds(s.sys)
		if err != nil {
			return err
		}
		in, p, err := loadPeriod(ctx, tx, r.SystemID, start, end)
		if err != nil {
			return err
		}
		in.System = s.sys
		if len(p.roots) == 0 {
			return ErrNoEvidence
		}
		ev, err := evaluate.Evaluate(in)
		if err != nil {
			return err
		}
		evalID, err := storeEvaluation(ctx, tx, r, ev, p.digests)
		if err != nil {
			return err
		}

		var seq int
		var prevDigest *string
		var prevEnv *dsse.Envelope
		var prevSeq int
		var lastDigest, lastEnvelope string
		switch err := tx.QueryRow(ctx, `SELECT seq_no, digest, envelope::text FROM packs WHERE ai_system_id = $1 ORDER BY seq_no DESC LIMIT 1`, r.SystemID).
			Scan(&prevSeq, &lastDigest, &lastEnvelope); {
		case errors.Is(err, pgx.ErrNoRows):
			seq = 1
		case err != nil:
			return err
		default:
			seq, prevDigest = prevSeq+1, &lastDigest
			if prevEnv, err = dsse.Decode([]byte(lastEnvelope)); err != nil {
				return fmt.Errorf("issue: the previous pack's envelope does not decode: %w", err)
			}
		}

		tree, err := merkle.NewTree(p.roots)
		if err != nil {
			return err
		}
		root := tree.Root()
		loc, _ := time.LoadLocation(s.sys.Timezone)
		issuedAt := now().In(loc).Truncate(time.Second)
		// The key must be registered and in date at issued_at, by the rule verifiers apply to the JWKS (D-042).
		if err := keys.CheckSigner(ctx, tx, is.Signer.KeyID(), is.Signer.PublicKey(), issuedAt); err != nil {
			return err
		}
		pred := predicate(s, ev, seq, prevDigest, issuedAt.Format(time.RFC3339),
			hex.EncodeToString(root[:]), p.leaves, p.digests, p.usageProviders, len(in.Changes)+len(in.Incidents) > 0)
		payload, err := statement(pred)
		if err != nil {
			return err
		}
		env, err := dsse.Sign(ctx, is.Signer, dsse.PayloadTypeInToto, payload)
		if err != nil {
			return err
		}
		// Verify before storing: a key mix-up or a broken chain never leaves this transaction.
		res, err := pack.Verify(env, pack.KeySetOf(map[string]crypto.PublicKey{is.Signer.KeyID(): is.Signer.PublicKey()}), prevEnv)
		if err != nil {
			return fmt.Errorf("issue: the pack does not decode as a v0.1 statement: %w", err)
		}
		if !res.Verified {
			return fmt.Errorf("issue: the new pack does not verify (signature %v, subject digest %v, chain %v)", res.Signature, res.SubjectDigest, res.Chain)
		}
		raw, err := env.Encode()
		if err != nil {
			return err
		}
		digest := pack.Digest(env)
		if _, err := tx.Exec(ctx, `INSERT INTO packs (tenant_id, ai_system_id, evaluation_id, pack_id, seq_no, prev_digest, digest, envelope, kms_key_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			r.TenantID, r.SystemID, evalID, pred.PackID, seq, prevDigest, digest, string(raw), is.Signer.KeyRef()); err != nil {
			return err
		}
		out = &Issued{PackID: pred.PackID, SeqNo: seq, Digest: digest, EvaluationID: evalID, Envelope: raw}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if is.Log != nil {
		is.Log.Info("pack issued", "tenant_id", r.TenantID, "ai_system_id", r.SystemID, "pack_id", out.PackID, "seq_no", out.SeqNo, "digest", out.Digest)
	}
	return out, nil
}

// loadSystem reads the system's declarations and locks its row: concurrent
// issues for one system wait here, so each sees the previous one's pack.
func loadSystem(ctx context.Context, tx pgx.Tx, r Request) (*systemRow, error) {
	var s systemRow
	var models, regions, profiles, providers, limitations []byte
	err := tx.QueryRow(ctx, `SELECT t.name, s.name, s.environment, s.description, s.declared_models, s.declared_regions,
			s.timezone, s.profiles, s.providers, s.limitations, s.pack_prefix
		FROM ai_systems s JOIN tenants t ON t.id = s.tenant_id WHERE s.id = $1 FOR UPDATE OF s`, r.SystemID).
		Scan(&s.sys.Organisation, &s.sys.AISystem, &s.sys.Environment, &s.sys.Description, &models, &regions,
			&s.sys.Timezone, &profiles, &providers, &limitations, &s.packPrefix)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", errNoSystem, r.SystemID)
	}
	if err != nil {
		return nil, err
	}
	for _, f := range []struct {
		raw []byte
		v   any
	}{{models, &s.sys.DeclaredModels}, {regions, &s.sys.DeclaredRegions}, {profiles, &s.sys.Profiles},
		{providers, &s.sys.Providers}, {limitations, &s.sys.Limitations}} {
		if err := json.Unmarshal(f.raw, f.v); err != nil {
			return nil, fmt.Errorf("issue: system declarations: %w", err)
		}
	}
	s.sys.Period = evaluate.Period{Start: r.PeriodStart, End: r.PeriodEnd}
	return &s, nil
}

type periodData struct {
	digests        []string
	roots          []merkle.Hash
	leaves         int
	usageProviders []string
}

func rfc3339(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z") }

// loadPeriod reads the batches whose window starts in [start, end), every
// register entry, and the eval runs. Provider usage is per account, and a
// restart or a second agent can repeat an hour's usage, so usage is
// deduplicated by provider, model, region and hour, keeping the largest (D-040).
func loadPeriod(ctx context.Context, tx pgx.Tx, systemID string, start, end time.Time) (evaluate.Input, *periodData, error) {
	var in evaluate.Input
	p := &periodData{}
	rows, err := tx.Query(ctx, `SELECT b.digest, b.payload::text FROM evidence_batches b JOIN agents a ON a.id = b.agent_id
		WHERE a.ai_system_id = $1 AND b.window_start >= $2 AND b.window_start < $3
		ORDER BY b.window_start, b.agent_id, b.seq`, systemID, start, end)
	if err != nil {
		return in, nil, err
	}
	var bs []batch.Batch
	providers := map[string]bool{}
	for rows.Next() {
		var digest, payload string
		if err := rows.Scan(&digest, &payload); err != nil {
			rows.Close()
			return in, nil, err
		}
		var b batch.Batch
		if err := json.Unmarshal([]byte(payload), &b); err != nil {
			rows.Close()
			return in, nil, fmt.Errorf("issue: stored batch %s: %w", digest, err)
		}
		bs = append(bs, b)
		p.digests = append(p.digests, digest)
		if b.Commit.Leaves > 0 {
			raw, err := hex.DecodeString(b.Commit.Root)
			if err != nil || len(raw) != 32 {
				rows.Close()
				return in, nil, fmt.Errorf("issue: stored batch %s has a malformed root", digest)
			}
			p.roots = append(p.roots, merkle.Hash(raw))
			p.leaves += int(b.Commit.Leaves)
		}
		for _, u := range b.Usage {
			providers[u.Provider] = true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return in, nil, err
	}
	if in.Windows, in.Usage, in.Evals, err = evaluate.FromBatches(bs); err != nil {
		return in, nil, err
	}
	for pr := range providers {
		p.usageProviders = append(p.usageProviders, pr)
	}
	slices.Sort(p.usageProviders)

	crow, err := tx.Query(ctx, `SELECT change_date::text, kind, what FROM register_changes WHERE ai_system_id = $1 ORDER BY change_date, id`, systemID)
	if err != nil {
		return in, nil, err
	}
	in.Changes, err = pgx.CollectRows(crow, func(r pgx.CollectableRow) (evaluate.Change, error) {
		var c evaluate.Change
		return c, r.Scan(&c.Date, &c.Kind, &c.What)
	})
	if err != nil {
		return in, nil, err
	}
	// The newest entry per incident wins: a later acknowledgement is a new row.
	irow, err := tx.Query(ctx, `SELECT incident_ref, opened, acknowledged, resolved, summary FROM (
			SELECT DISTINCT ON (incident_ref) * FROM register_incidents WHERE ai_system_id = $1 ORDER BY incident_ref, recorded_at DESC, id DESC
		) latest ORDER BY opened, incident_ref`, systemID)
	if err != nil {
		return in, nil, err
	}
	in.Incidents, err = pgx.CollectRows(irow, func(r pgx.CollectableRow) (evaluate.Incident, error) {
		var i evaluate.Incident
		var opened time.Time
		var ack, res *time.Time
		if err := r.Scan(&i.ID, &opened, &ack, &res, &i.Summary); err != nil {
			return i, err
		}
		i.Opened = rfc3339(opened)
		if ack != nil {
			i.Acknowledged = rfc3339(*ack)
		}
		if res != nil {
			i.Resolved = rfc3339(*res)
		}
		return i, nil
	})
	if err != nil {
		return in, nil, err
	}
	erow, err := tx.Query(ctx, `SELECT suite, model, prompt_version, ran_at, pass_rate FROM register_evals WHERE ai_system_id = $1 ORDER BY ran_at, id`, systemID)
	if err != nil {
		return in, nil, err
	}
	evals, err := pgx.CollectRows(erow, func(r pgx.CollectableRow) (batch.Eval, error) {
		var e batch.Eval
		var at time.Time
		err := r.Scan(&e.Suite, &e.Model, &e.PromptVersion, &at, &e.PassRate)
		e.RanAt = rfc3339(at)
		return e, err
	})
	if err != nil {
		return in, nil, err
	}
	in.Evals = append(in.Evals, evals...)
	return in, p, nil
}

func storeEvaluation(ctx context.Context, tx pgx.Tx, r Request, ev *evaluate.Evaluation, digests []string) (string, error) {
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO evaluations (tenant_id, ai_system_id, period_start, period_end, objective_set_hash)
		VALUES ($1, $2, $3, $4, $5) RETURNING id`, r.TenantID, r.SystemID, ev.PeriodStart, ev.PeriodEnd, ev.ObjectiveSetHash).Scan(&id); err != nil {
		return "", err
	}
	if digests == nil {
		digests = []string{}
	}
	for _, o := range ev.Objectives {
		measured, err := json.Marshal(map[string]any{"measured": o.Measured, "threshold": o.Threshold, "values": o.Values})
		if err != nil {
			return "", err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO objective_results (tenant_id, evaluation_id, eo_id, eo_version, status, measured, batch_digests)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`, r.TenantID, id, o.ID, o.Version, o.Status, string(measured), digests); err != nil {
			return "", err
		}
	}
	return id, nil
}

func predicate(s *systemRow, ev *evaluate.Evaluation, seq int, prev *string, issuedAt, root string, leaves int,
	digests, usageProviders []string, hasRegisters bool) pack.Predicate {
	p := pack.Predicate{
		PackSchema:       pack.PackSchema,
		PackID:           fmt.Sprintf("%s-%s-%04d", s.packPrefix, packStem(s), seq),
		SeqNo:            seq,
		PrevPackDigest:   prev,
		IssuedAt:         issuedAt,
		Period:           pack.Period{Start: ev.Period["start"], End: ev.Period["end"], Timezone: ev.Period["timezone"]},
		Subject:          pack.PackSubject{Organisation: s.sys.Organisation, AISystem: s.sys.AISystem, Environment: s.sys.Environment, Description: s.sys.Description},
		Profiles:         ev.Profiles,
		AssuranceLevel:   assurance,
		Coverage:         pack.Coverage{Basis: ev.Coverage.Basis, TotalPct: ev.Coverage.TotalPct, Providers: []pack.ProviderCoverage{}},
		Lineage:          []pack.Lineage{},
		Changes:          []pack.Change{},
		Incidents:        []pack.Incident{},
		Limitations:      ev.Limitations,
		Commitments:      pack.Commitments{TraceMerkleRoot: root, LeafCount: leaves, Leaf: LeafRule, Node: NodeRule},
		ObjectiveSetHash: ev.ObjectiveSetHash,
		BatchDigests:     digests,
	}
	p.Sources = []pack.Source{{Kind: "traces", System: "Tellproof evidence agent", Method: "Hourly aggregates signed by the agent (Ed25519); message content dropped at parse"}}
	for _, pr := range usageProviders {
		name := pr
		if n, ok := s.sys.Providers[pr]; ok {
			name = n
		}
		p.Sources = append(p.Sources, pack.Source{Kind: "provider_usage", System: name + " usage", Method: "Billed usage polled hourly by the evidence agent"})
	}
	if hasRegisters {
		p.Sources = append(p.Sources, pack.Source{Kind: "registers", System: "Tellproof change and incident registers", Method: "Entered by the vendor in the Tellproof web app"})
	}
	for _, c := range ev.Coverage.Providers {
		tr := c.TracedRequests
		p.Coverage.Providers = append(p.Coverage.Providers, pack.ProviderCoverage{Provider: c.Provider, Region: c.Region,
			BilledTokens: c.BilledTokens, TracedTokens: c.TracedTokens, BilledRequests: c.BilledRequests, TracedRequests: &tr})
	}
	for _, l := range ev.Lineage {
		p.Lineage = append(p.Lineage, pack.Lineage(l))
	}
	for _, c := range ev.Changes {
		p.Changes = append(p.Changes, pack.Change(c))
	}
	for _, i := range ev.Incidents {
		p.Incidents = append(p.Incidents, pack.Incident{ID: i["id"], Opened: i["opened"], Acknowledged: i["acknowledged"], Resolved: i["resolved"], Summary: i["summary"]})
	}
	for _, o := range ev.Objectives {
		p.Objectives = append(p.Objectives, pack.Objective{ID: o.ID, Title: o.Title, Status: o.Status, Threshold: o.Threshold,
			Measured: o.Measured, Basis: o.Basis, Maps: o.Maps})
	}
	if p.Profiles == nil {
		p.Profiles = []string{}
	}
	return p
}

// packStem is a short, stable, upper-case tag for the system in pack IDs.
func packStem(s *systemRow) string {
	sum := sha256.Sum256([]byte(s.sys.Organisation + "\x00" + s.sys.AISystem + "\x00" + s.sys.Environment))
	return fmt.Sprintf("%X", sum[:3])
}

// statement is JCS({_type, subject: [{name, digest: {sha256: hex(sha256(JCS(predicate)))}}], predicateType, predicate}).
func statement(p pack.Predicate) ([]byte, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	canonPred, err := jcs.Canonicalize(raw)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(canonPred)
	st := map[string]any{
		"_type":         pack.StatementType,
		"subject":       []any{map[string]any{"name": p.PackID, "digest": map[string]string{"sha256": hex.EncodeToString(sum[:])}}},
		"predicateType": pack.PredicateType,
		"predicate":     json.RawMessage(canonPred),
	}
	raw, err = json.Marshal(st)
	if err != nil {
		return nil, err
	}
	return jcs.Canonicalize(raw)
}

// EnsureObjectiveDefs stores the embedded objectives in objective_defs, once
// per id and version, with the SHA-256 of each definition's JCS form, so every
// stored evaluation can be read against the exact rules it used.
func EnsureObjectiveDefs(ctx context.Context, s *store.Store) error {
	var doc struct {
		Objectives []json.RawMessage `json:"objectives"`
	}
	if err := json.Unmarshal([]byte(specdata.Objectives), &doc); err != nil {
		return err
	}
	return s.Public(ctx, func(tx pgx.Tx) error {
		for _, raw := range doc.Objectives {
			var o struct {
				ID      string `json:"id"`
				Version int    `json:"version"`
			}
			if err := json.Unmarshal(raw, &o); err != nil {
				return err
			}
			c, err := jcs.Canonicalize(raw)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(c)
			if _, err := tx.Exec(ctx, `INSERT INTO objective_defs (eo_id, version, definition, hash) VALUES ($1, $2, $3, $4)
				ON CONFLICT (eo_id, version) DO NOTHING`, o.ID, o.Version, string(c), "sha256:"+hex.EncodeToString(sum[:])); err != nil {
				return err
			}
		}
		return nil
	})
}

// JobPayload is an issue_pack job's payload; the tenant is the job's.
type JobPayload struct {
	AISystemID  string `json:"ai_system_id"`
	PeriodStart string `json:"period_start"`
	PeriodEnd   string `json:"period_end"`
}

// Handler is the issue_pack job handler. A request that can never succeed
// (bad payload, no evidence, unknown system) fails permanently; anything else
// (KMS or database trouble) is retried. Running it twice issues two packs, each
// correctly chained, so a retry after a lost acknowledgement does no harm.
func (is *Issuer) Handler() jobs.Handler {
	return func(ctx context.Context, j jobs.Job) error {
		var p JobPayload
		if err := json.Unmarshal(j.Payload, &p); err != nil || j.TenantID == nil || p.AISystemID == "" || p.PeriodStart == "" || p.PeriodEnd == "" {
			return jobs.Permanent(errors.New("issue_pack: the payload needs ai_system_id, period_start and period_end, and the job a tenant"))
		}
		_, err := is.Issue(ctx, Request{TenantID: *j.TenantID, SystemID: p.AISystemID, PeriodStart: p.PeriodStart, PeriodEnd: p.PeriodEnd})
		if errors.Is(err, ErrNoEvidence) || errors.Is(err, errNoSystem) || errors.Is(err, evaluate.ErrPeriod) {
			return jobs.Permanent(err)
		}
		return err
	}
}
