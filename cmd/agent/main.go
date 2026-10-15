// Command plumbline-agent is the Plumbline evidence agent. It runs next to an
// AI product, turns its telemetry into signed hourly aggregates and sends only
// those (docs/architecture.md §3).
//
//	plumbline-agent enrol --config agent.json      # token in PLUMBLINE_ENROL_TOKEN
//	plumbline-agent [run] --config agent.json [--dry-run] [--replay export.jsonl]
//	plumbline-agent version
//
// --dry-run prints every batch it would send, with its payload decoded, and
// sends, queues and stores nothing. --replay feeds an OTLP JSON export through
// the same pipeline, emits every window and exits.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lumetrix-advanced-technologies/plumbline/internal/agent"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/enrol"
)

// version is set at link time: go build -ldflags "-X main.version=v0.1.0".
var version = "dev"

const usage = `usage:
  plumbline-agent enrol --config <agent.json> [--token <token>]
  plumbline-agent [run] --config <agent.json> [--dry-run] [--replay <export.jsonl>]
  plumbline-agent version

The enrolment token is read from PLUMBLINE_ENROL_TOKEN when --token is not
given. Secrets are read from the environment variables the config names.
Exit status: 0 ok, 1 runtime error, 2 usage or config error.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	cmd := "run"
	if len(args) > 0 && (args[0] == "run" || args[0] == "enrol" || args[0] == "version") {
		cmd, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("plumbline-agent "+cmd, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configPath := fs.String("config", "", "agent config file")
	dryRun := fs.Bool("dry-run", false, "print what would be sent; send, queue and store nothing")
	replay := fs.String("replay", "", "feed an OTLP JSON export through the pipeline and exit")
	token := fs.String("token", "", "one-time enrolment token (default $PLUMBLINE_ENROL_TOKEN)")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	log := slog.New(slog.NewJSONHandler(stderr, nil))
	switch cmd {
	case "version":
		fmt.Fprintf(stdout, "plumbline-agent %s\n", version)
		return 0
	case "enrol":
		if *dryRun || *replay != "" {
			fmt.Fprint(stderr, usage)
			return 2
		}
		return doEnrol(ctx, *configPath, *token, getenv, stdout, stderr)
	}
	if *token != "" {
		fmt.Fprint(stderr, usage)
		return 2
	}
	cfg, err := agent.LoadConfig(*configPath, getenv)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	a, err := agent.New(cfg, agent.Options{DryRun: *dryRun, Out: stdout, Log: log, Getenv: getenv})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if *replay != "" {
		if err := doReplay(ctx, a, *replay); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	if err := a.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("agent stopped", "error", err.Error())
		return 1
	}
	return 0
}

func doReplay(ctx context.Context, a *agent.Agent, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := a.Replay(f); err != nil {
		return err
	}
	if err := a.Shutdown(ctx); err != nil {
		return err
	}
	return a.Deliver(ctx)
}

func doEnrol(ctx context.Context, configPath, token string, getenv func(string) string, stdout, stderr io.Writer) int {
	if token == "" {
		token = getenv("PLUMBLINE_ENROL_TOKEN")
	}
	if token == "" {
		fmt.Fprintln(stderr, "plumbline-agent: enrol needs a token: set PLUMBLINE_ENROL_TOKEN or pass --token")
		return 2
	}
	cfg, err := agent.LoadConfig(configPath, getenv)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if cfg.Server == "" {
		fmt.Fprintln(stderr, "plumbline-agent: enrol needs a server in the config or PLUMBLINE_SERVER")
		return 2
	}
	id, err := enrol.Enrol(ctx, enrol.Options{Server: cfg.Server, Token: token, DataDir: cfg.DataDir, HTTP: &http.Client{Timeout: 30 * time.Second}})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "enrolled as %s with %s; identity in %s\n", id.AgentID, id.Server, cfg.DataDir)
	return 0
}
