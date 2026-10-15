// Command plumbline-server is the Plumbline control plane: one binary,
// subcommands (docs/architecture.md §4.1).
//
//	plumbline-server migrate   # apply migrations/ as the schema owner (DATABASE_URL_OWNER)
//	plumbline-server version
//
// serve (API, web app, verify API) and work (jobs) arrive with P4-2 and P4-3.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/store"
	"github.com/lumetrix-advanced-technologies/plumbline/migrations"
)

// version is set at link time: go build -ldflags "-X main.version=v0.1.0".
var version = "dev"

const usage = `usage:
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
