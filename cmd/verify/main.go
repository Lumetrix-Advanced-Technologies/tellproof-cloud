// Command plumbline-verify checks Plumbline evidence packs offline.
//
//	plumbline-verify --jwks <file|https-url> pack1.json [pack2.json ...]
//
// Packs are given oldest first; each one after the first is checked against
// the one before it. It exits 0 only when every pack verifies, 1 when any pack
// fails, and 2 for usage or input errors.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/lumetrix-advanced-technologies/plumbline/internal/dsse"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/pack"
)

// version is set at link time: go build -ldflags "-X main.version=v0.1.0".
var version = "dev"

const (
	maxJWKSBytes = 1 << 20  // 1 MB
	maxPackBytes = 16 << 20 // 16 MB; packs are about 12 KB
)

type config struct {
	version string
	client  *http.Client
}

func main() {
	cfg := config{version: version, client: &http.Client{Timeout: 10 * time.Second}}
	os.Exit(run(context.Background(), cfg, os.Args[1:], os.Stdout, os.Stderr))
}

const usage = `usage: plumbline-verify --jwks <file|https-url> [--json] pack1.json [pack2.json ...]

Verifies Plumbline evidence packs: the issuer's signature, the subject digest,
the link to the previous pack and the disclosed trace's Merkle proof.
Give packs oldest first. Exit status: 0 all verified, 1 a pack failed, 2 usage
or input error.
`

// usageError is a problem with the command line or its inputs (exit 2).
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func run(ctx context.Context, cfg config, args []string, stdout, stderr io.Writer) int {
	opts, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "plumbline-verify: %v\n\n%s", err, usage)
		return 2
	}
	if opts.version {
		fmt.Fprintf(stdout, "plumbline-verify %s\n", cfg.version)
		return 0
	}
	keys, err := loadJWKS(ctx, cfg.client, opts.jwks)
	if err != nil {
		fmt.Fprintf(stderr, "plumbline-verify: %v\n", err)
		return 2
	}

	var reports []packReport
	var prev *dsse.Envelope
	for i, file := range opts.files {
		data, err := readFile(file, maxPackBytes)
		if err != nil {
			fmt.Fprintf(stderr, "plumbline-verify: %v\n", err)
			return 2
		}
		rep := verifyOne(file, data, keys, prev)
		// The previous file was not an envelope, so nothing can link to it (D-017).
		if i > 0 && prev == nil && rep.Error == "" {
			broken := false
			rep.Chain, rep.Verified = &broken, false
		}
		reports = append(reports, rep)
		prev = rep.env
	}

	verified := true
	for _, r := range reports {
		verified = verified && r.Verified
	}
	if opts.json {
		writeJSON(stdout, verified, reports)
	} else {
		writeHuman(stdout, verified, reports)
	}
	if verified {
		return 0
	}
	return 1
}

type options struct {
	jwks    string
	json    bool
	version bool
	files   []string
}

// parseArgs accepts flags before, between or after the pack files.
func parseArgs(args []string) (options, error) {
	var o options
	fs := flag.NewFlagSet("plumbline-verify", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.jwks, "jwks", "", "issuer JWKS file or https URL")
	fs.BoolVar(&o.json, "json", false, "print a JSON report")
	fs.BoolVar(&o.version, "version", false, "print the version")
	if len(args) == 0 {
		return o, usageError{"usage"}
	}
	for {
		if err := fs.Parse(args); err != nil {
			return o, usageError{err.Error()}
		}
		args = fs.Args()
		if len(args) == 0 {
			break
		}
		o.files = append(o.files, args[0])
		args = args[1:]
	}
	switch {
	case o.version:
		return o, nil
	case o.jwks == "":
		return o, usageError{"--jwks is required"}
	case len(o.files) == 0:
		return o, usageError{"no pack files given"}
	}
	return o, nil
}

func loadJWKS(ctx context.Context, client *http.Client, src string) (*pack.KeySet, error) {
	var data []byte
	var err error
	switch {
	case strings.HasPrefix(src, "https://"):
		data, err = fetch(ctx, client, src)
	case strings.Contains(src, "://"):
		return nil, usageError{fmt.Sprintf("jwks: %s: only https URLs are fetched", src)}
	default:
		data, err = readFile(src, maxJWKSBytes)
	}
	if err != nil {
		return nil, fmt.Errorf("jwks: %w", err)
	}
	keys, err := pack.ParseJWKS(data)
	if err != nil {
		return nil, err
	}
	return keys, nil
}

func fetch(ctx context.Context, client *http.Client, rawURL string) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", u.Redacted(), resp.StatusCode)
	}
	return readLimited(resp.Body, maxJWKSBytes, u.Redacted())
}

func readFile(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readLimited(f, limit, path)
}

func readLimited(r io.Reader, limit int64, name string) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s: larger than %d bytes", name, limit)
	}
	return data, nil
}

type packReport struct {
	File string `json:"file"`
	pack.Result
	SignatureError string `json:"signature_error,omitempty"`
	Error          string `json:"error,omitempty"`

	env *dsse.Envelope
}

func verifyOne(file string, data []byte, keys *pack.KeySet, prev *dsse.Envelope) packReport {
	rep := packReport{File: file}
	env, err := dsse.Decode(data)
	if err != nil {
		rep.Error = err.Error()
		return rep
	}
	rep.env = env
	res, err := pack.Verify(env, keys, prev)
	rep.Result = res
	if err != nil {
		rep.Error = err.Error()
		rep.Verified = false
	}
	if res.SignatureErr != nil {
		rep.SignatureError = res.SignatureErr.Error()
	}
	return rep
}

func writeJSON(w io.Writer, verified bool, reports []packReport) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(struct {
		Verified bool         `json:"verified"`
		Packs    []packReport `json:"packs"`
	}{verified, reports})
}

func writeHuman(w io.Writer, verified bool, reports []packReport) {
	line := func(label, text string) {
		fmt.Fprintf(w, "  %s %s %s\n", label, strings.Repeat(".", 21-len(label)), text) // concierge/verify.py's layout
	}
	for _, r := range reports {
		fmt.Fprintln(w, r.File)
		if r.Error != "" {
			line("pack", "FAILED: not a valid pack: "+r.Error)
			line("result", "FAILED (not a valid pack)")
			continue
		}
		line("pack", fmt.Sprintf("%s (seq %d)", r.PackID, r.SeqNo))
		line("digest", r.Digest)

		var failed []string
		if r.Signature {
			line("signature", "valid (key "+r.KeyID+")")
		} else {
			line("signature", "FAILED: "+strings.TrimPrefix(strings.TrimPrefix(r.SignatureError, "dsse: "), "pack: "))
			failed = append(failed, "signature")
		}
		if r.SubjectDigest {
			line("subject digest", "matches the pack contents")
		} else {
			line("subject digest", "FAILED: does not match the pack contents; the pack was changed after its digest was taken")
			failed = append(failed, "subject digest")
		}
		switch {
		case r.Chain == nil:
			line("chain link", "not checked (no previous pack given)")
		case *r.Chain:
			line("chain link", "links to the previous pack")
		default:
			line("chain link", "FAILED: does not link to the previous pack")
			failed = append(failed, "chain link")
		}
		switch {
		case r.Merkle == nil:
			line("disclosed trace", "not checked (no trace disclosed)")
		case *r.Merkle:
			line("disclosed trace", "included in the commitment")
		default:
			line("disclosed trace", "FAILED: proof does not reach the commitment root")
			failed = append(failed, "disclosed trace")
		}
		if len(failed) == 0 {
			line("result", "VERIFIED")
		} else {
			line("result", "FAILED ("+strings.Join(failed, ", ")+")")
		}
	}
	if verified {
		fmt.Fprintln(w, "RESULT: VERIFIED")
	} else {
		fmt.Fprintln(w, "RESULT: FAILED")
	}
}
