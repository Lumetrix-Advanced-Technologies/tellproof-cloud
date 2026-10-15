// Command plumbline-server is the Plumbline control plane: one binary,
// subcommands (docs/architecture.md §4.1).
//
//	plumbline-server serve     # the API (DATABASE_URL as plumbline_app; LISTEN, default :8080)
//	plumbline-server migrate   # apply migrations/ as the schema owner (DATABASE_URL_OWNER)
//	plumbline-server version
//
// work (jobs) arrives with P4-3.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/server"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/store"
	"github.com/lumetrix-advanced-technologies/plumbline/migrations"
)

// version is set at link time: go build -ldflags "-X main.version=v0.1.0".
var version = "dev"

const usage = `usage:
  plumbline-server serve      run the API (DATABASE_URL; optional DATABASE_ROLE, LISTEN)
  plumbline-server migrate    apply migrations as the schema owner (DATABASE_URL_OWNER)
  plumbline-server version
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
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
	srv := &http.Server{Handler: (&server.Server{Store: st, Log: log}).Handler(),
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
