// Command plumbline-server is the Plumbline control plane: one binary,
// subcommands (docs/architecture.md §4.1).
//
//	plumbline-server serve     # the API (DATABASE_URL as plumbline_app; LISTEN, default :8080)
//	plumbline-server work      # run queued jobs (same DATABASE_URL and DATABASE_ROLE; KMS_KEY_ID to issue
//	                           # packs and checkpoints; CHECKPOINT_REPO and GITHUB_TOKEN to publish them)
//	plumbline-server migrate   # apply migrations/ as the schema owner (DATABASE_URL_OWNER)
//	plumbline-server keys ...  # add, retire, revoke and list pack signing keys (owner)
//	plumbline-server version
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/jackc/pgx/v5"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/dailycheckpoint"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/issue"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/jobs"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/keys"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/kmssign"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/server"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/store"
	"github.com/lumetrix-advanced-technologies/plumbline/migrations"
)

// version is set at link time: go build -ldflags "-X main.version=v0.1.0".
var version = "dev"

const usage = `usage:
  plumbline-server serve      run the API (DATABASE_URL; optional DATABASE_ROLE, LISTEN)
  plumbline-server work       run queued jobs (DATABASE_URL; optional DATABASE_ROLE)
  plumbline-server migrate    apply migrations as the schema owner (DATABASE_URL_OWNER)
  plumbline-server keys list | add [--nbf T] | retire KID [--at T] | revoke KID --reason TEXT
                              manage the pack signing keys (DATABASE_URL_OWNER; add signs with KMS_KEY_ID)
  plumbline-server version
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if len(args) >= 1 && args[0] == "keys" {
		return keysCmd(ctx, args[1:], getenv, stdout, stderr)
	}
	if len(args) != 1 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	log := slog.New(slog.NewJSONHandler(stderr, nil))
	switch args[0] {
	case "version":
		fmt.Fprintf(stdout, "plumbline-server %s\n", version)
		return 0
	case "serve":
		return serve(ctx, getenv, log, stderr)
	case "work":
		return work(ctx, getenv, log, stderr)
	case "migrate":
		url := getenv("DATABASE_URL_OWNER")
		if url == "" {
			fmt.Fprintln(stderr, "plumbline-server: DATABASE_URL_OWNER is not set")
			return 2
		}
		conn, err := pgx.Connect(ctx, url)
		if err != nil {
			log.Error("migrate: cannot connect to the database") // the URL holds a password: never logged
			return 1
		}
		defer conn.Close(context.WithoutCancel(ctx))
		ms, err := store.LoadMigrations(migrations.Files)
		if err != nil {
			log.Error("migrate", "error", err.Error())
			return 1
		}
		done, err := store.Migrate(ctx, conn, ms)
		for _, name := range done {
			log.Info("migration applied", "name", name)
		}
		if err != nil {
			log.Error("migrate", "error", err.Error())
			return 1
		}
		log.Info("migrations up to date", "applied", len(done), "total", len(ms))
		return 0
	}
	fmt.Fprint(stderr, usage)
	return 2
}

// serve runs the API until ctx ends, then drains in-flight requests.
// DATABASE_URL connects as plumbline_app; DATABASE_ROLE (development only)
// makes an owner connection assume it.
func serve(ctx context.Context, getenv func(string) string, log *slog.Logger, stderr io.Writer) int {
	url := getenv("DATABASE_URL")
	if url == "" {
		fmt.Fprintln(stderr, "plumbline-server: DATABASE_URL is not set")
		return 2
	}
	listen := getenv("LISTEN")
	if listen == "" {
		listen = ":8080"
	}
	st, err := store.Open(ctx, url, store.Options{Role: getenv("DATABASE_ROLE")})
	if err != nil {
		log.Error("serve: cannot connect to the database")
		return 1
	}
	defer st.Close()
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		log.Error("serve: cannot listen", "listen", listen, "error", err.Error())
		return 1
	}
	viewSecret := []byte(getenv("VIEW_HASH_SECRET"))
	if len(viewSecret) < 16 {
		viewSecret = make([]byte, 32)
		_, _ = rand.Read(viewSecret)
		log.Warn("VIEW_HASH_SECRET is unset or short: viewer counts reset when this process restarts")
	}
	api := &server.Server{Store: st, Log: log, ViewSecret: viewSecret, ClientIPHeader: getenv("CLIENT_IP_HEADER")}
	srv := &http.Server{Handler: api.Handler(),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 120 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	log.Info("serving", "listen", ln.Addr().String(), "version", version)
	select {
	case err := <-errc:
		log.Error("serve stopped", "error", err.Error())
		return 1
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("shutdown", "error", err.Error())
		return 1
	}
	log.Info("stopped")
	return 0
}

// work runs the job worker until ctx ends; a job in progress finishes first.
// Each later task registers its job kind here (evaluate, issue_pack,
// checkpoint, vanta_sync).
func work(ctx context.Context, getenv func(string) string, log *slog.Logger, stderr io.Writer) int {
	url := getenv("DATABASE_URL")
	if url == "" {
		fmt.Fprintln(stderr, "plumbline-server: DATABASE_URL is not set")
		return 2
	}
	st, err := store.Open(ctx, url, store.Options{Role: getenv("DATABASE_ROLE")})
	if err != nil {
		log.Error("work: cannot connect to the database")
		return 1
	}
	defer st.Close()
	if err := jobs.EnsureScheduled(ctx, st, jobs.KindPruneJobs, time.Time{}); err != nil {
		log.Error("work: scheduling prune_jobs", "error", err.Error())
		return 1
	}
	if err := issue.EnsureObjectiveDefs(ctx, st); err != nil {
		log.Error("work: storing objective definitions", "error", err.Error())
		return 1
	}
	handlers := map[string]jobs.Handler{jobs.KindPruneJobs: jobs.Prune(st, 30*24*time.Hour, 24*time.Hour)}
	signer, err := packSigner(ctx, getenv)
	switch {
	case err != nil:
		log.Error("work: pack signer", "error", err.Error())
		return 1
	case signer == nil:
		log.Warn("issue_pack is off: KMS_KEY_ID is not set")
	default:
		handlers[jobs.KindIssuePack] = (&issue.Issuer{Store: st, Signer: signer, Log: log}).Handler()
		log.Info("pack signer ready", "kid", signer.KeyID())
		cp := &dailycheckpoint.Job{Store: st, Signer: signer, Log: log}
		if repo, token := getenv("CHECKPOINT_REPO"), getenv("GITHUB_TOKEN"); repo != "" && token != "" {
			cp.Publisher = &dailycheckpoint.GitHub{Repo: repo, Token: token}
		} else {
			log.Warn("checkpoints are signed and stored but not published: CHECKPOINT_REPO or GITHUB_TOKEN is not set")
		}
		handlers[jobs.KindCheckpoint] = cp.Handler()
		if err := jobs.EnsureScheduled(ctx, st, jobs.KindCheckpoint, cp.NextRun()); err != nil {
			log.Error("work: scheduling checkpoint", "error", err.Error())
			return 1
		}
	}
	w := &jobs.Worker{Store: st, Log: log, Handlers: handlers}
	if err := w.Run(ctx); err != nil {
		log.Error("work stopped", "error", err.Error())
		return 1
	}
	log.Info("worker stopped")
	return 0
}

// packSigner is the pack signing key: KMS_KEY_ID is a KMS key ARN (region
// from AWS_REGION and the default AWS credential chain), or "local" for a
// P-256 PEM at LOCAL_SIGNING_KEY (default .dev/local-signing.pem) in
// development. Unset: no signer.
func packSigner(ctx context.Context, getenv func(string) string) (kmssign.Signer, error) {
	id := getenv("KMS_KEY_ID")
	switch id {
	case "":
		return nil, nil
	case "local":
		path := getenv("LOCAL_SIGNING_KEY")
		if path == "" {
			path = ".dev/local-signing.pem"
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("local signing key: %w", err)
		}
		return kmssign.NewLocal(b)
	}
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(getenv("AWS_REGION")))
	if err != nil {
		return nil, fmt.Errorf("AWS configuration: %w", err)
	}
	return kmssign.NewKMS(ctx, kms.NewFromConfig(cfg), id)
}

// keysCmd manages signing keys with the owner role: the app role can only
// read them, so adding, retiring and revoking are deliberate ops steps (D-042).
func keysCmd(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	bad := func() int { fmt.Fprint(stderr, usage); return 2 }
	if len(args) == 0 {
		return bad()
	}
	fs := flag.NewFlagSet("keys "+args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	nbfFlag := fs.String("nbf", "", "valid from (RFC 3339; default now)")
	atFlag := fs.String("at", "", "retire at (RFC 3339; default now)")
	reason := fs.String("reason", "", "why the key is revoked")
	sub, rest := args[0], args[1:]
	var kid string
	if (sub == "retire" || sub == "revoke") && len(rest) > 0 {
		kid, rest = rest[0], rest[1:]
	}
	if err := fs.Parse(rest); err != nil || fs.NArg() > 0 {
		return bad()
	}
	when := func(s string) (time.Time, error) {
		if s == "" {
			return time.Now(), nil
		}
		return time.Parse(time.RFC3339, s)
	}
	url := getenv("DATABASE_URL_OWNER")
	if url == "" {
		fmt.Fprintln(stderr, "plumbline-server: DATABASE_URL_OWNER is not set")
		return 2
	}
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		fmt.Fprintln(stderr, "plumbline-server: cannot connect to the database")
		return 1
	}
	defer conn.Close(context.WithoutCancel(ctx))
	err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		switch sub {
		case "list":
			rows, err := keys.List(ctx, tx)
			for _, r := range rows {
				state := "active"
				switch {
				case r.RevokedAt != nil:
					state = "revoked " + r.RevokedAt.UTC().Format(time.RFC3339) + " (" + r.Reason + ")"
				case r.Expires != nil:
					state = "retired " + r.Expires.UTC().Format(time.RFC3339)
				}
				fmt.Fprintf(stdout, "%s  %s  from %s  %s\n", r.KID, r.KeyRef, r.NotBefore.UTC().Format(time.RFC3339), state)
			}
			return err
		case "add":
			t, err := when(*nbfFlag)
			if err != nil {
				return err
			}
			signer, err := packSigner(ctx, getenv)
			if err != nil {
				return err
			}
			if signer == nil {
				return errors.New("KMS_KEY_ID is not set")
			}
			if err := keys.Add(ctx, tx, signer, t); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "added %s (%s), valid from %s\n", signer.KeyID(), signer.KeyRef(), t.UTC().Format(time.RFC3339))
			return nil
		case "retire":
			t, err := when(*atFlag)
			if err != nil || kid == "" {
				return errors.New("usage: keys retire KID [--at RFC3339]")
			}
			if err := keys.Retire(ctx, tx, kid, t); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "retired %s at %s: it signs nothing issued from then on; earlier packs still verify\n", kid, t.UTC().Format(time.RFC3339))
			return nil
		case "revoke":
			if kid == "" || *reason == "" {
				return errors.New("usage: keys revoke KID --reason TEXT")
			}
			if err := keys.Revoke(ctx, tx, kid, *reason); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "revoked %s: every pack it signed now fails verification; reissue them with another key\n", kid)
			return nil
		}
		return errors.New("unknown keys subcommand " + sub)
	})
	if err != nil {
		fmt.Fprintln(stderr, "plumbline-server keys:", err)
		return 1
	}
	return 0
}
