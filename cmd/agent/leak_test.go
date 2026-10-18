package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// leakCorpus is spec/leak-corpus.json (D-065).
type leakCorpus struct {
	Markers []struct {
		Kind, Needle, Text string
	} `json:"markers"`
	ContentAttributes []string `json:"content_attributes"`
	FactAttributes    []string `json:"fact_attributes"`
	TraceIDs          []string `json:"trace_ids"`
}

func loadLeakCorpus(t *testing.T) leakCorpus {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "spec", "leak-corpus.json"))
	if err != nil {
		t.Fatal(err)
	}
	var c leakCorpus
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	for _, m := range c.Markers {
		if m.Needle == "" || !strings.Contains(m.Text, m.Needle) {
			t.Fatalf("marker %s: needle %q is not in its text", m.Kind, m.Needle)
		}
	}
	return c
}

func str(v string) map[string]any { return map[string]any{"stringValue": v} }

func kv(k string, v map[string]any) map[string]any { return map[string]any{"key": k, "value": v} }

// leakExports builds one OTLP JSON export per marker that puts the marker in
// every place a span can carry text: every content attribute spelling (as a
// string, an array and a key-value list), the span and event names, event
// attributes, the status message, links, the scope and the resource, and,
// one span each, every attribute whose value the agent reads.
func leakExports(c leakCorpus, start time.Time) [][]byte {
	var out [][]byte
	ns := func(d time.Duration) string { return fmt.Sprint(start.Add(d).UnixNano()) }
	for i, m := range c.Markers {
		traceID := c.TraceIDs[i%len(c.TraceIDs)]
		spanID := func(n int) string { return fmt.Sprintf("%08x%08x", i+1, n+1) }
		base := []any{kv("gen_ai.operation.name", str("chat")), kv("gen_ai.provider.name", str("openai")),
			kv("gen_ai.request.model", str("gpt-4.1-mini")), kv("gen_ai.usage.input_tokens", map[string]any{"intValue": "10"}),
			kv("gen_ai.usage.output_tokens", map[string]any{"intValue": 5})}
		content := append([]any{}, base...)
		for _, a := range c.ContentAttributes {
			content = append(content, kv(a, str(m.Text)))
		}
		content = append(content,
			kv("gen_ai.input.messages", map[string]any{"arrayValue": map[string]any{"values": []any{str(m.Text)}}}),
			kv("gen_ai.output.messages", map[string]any{"kvlistValue": map[string]any{"values": []any{kv("content", str(m.Text))}}}),
			kv("tellproof.unmapped."+m.Kind, str(m.Text)))
		spans := []any{map[string]any{
			"traceId": traceID, "spanId": spanID(0), "parentSpanId": spanID(99), "name": m.Text, "kind": 3,
			"startTimeUnixNano": ns(0), "endTimeUnixNano": ns(time.Second), "attributes": content,
			"events": []any{
				map[string]any{"name": m.Text, "timeUnixNano": ns(time.Millisecond), "attributes": []any{kv("gen_ai.prompt", str(m.Text)), kv("exception.message", str(m.Text))}},
				map[string]any{"name": "gen_ai.content.completion", "timeUnixNano": ns(2 * time.Millisecond), "attributes": []any{kv("gen_ai.completion", str(m.Text))}},
			},
			"links":  []any{map[string]any{"traceId": traceID, "spanId": spanID(98), "attributes": []any{kv("link.note", str(m.Text))}}},
			"status": map[string]any{"code": 2, "message": m.Text},
		}}
		for j, a := range c.FactAttributes {
			attrs := append([]any{}, base...)
			attrs = append(attrs, kv(a, str(m.Text)))
			spans = append(spans, map[string]any{"traceId": traceID, "spanId": spanID(j + 1), "name": "chat",
				"startTimeUnixNano": ns(time.Duration(j+1) * time.Second), "endTimeUnixNano": ns(time.Duration(j+2) * time.Second), "attributes": attrs})
		}
		res := []any{kv("service.name", str("leak-test")), kv("host.name", str(m.Text)), kv("gen_ai.prompt", str(m.Text))}
		body, _ := json.Marshal(map[string]any{"resourceSpans": []any{map[string]any{
			"resource":   map[string]any{"attributes": res},
			"scopeSpans": []any{map[string]any{"scope": map[string]any{"name": m.Text, "attributes": []any{kv("scope.note", str(m.Text))}}, "spans": spans}},
		}}})
		out = append(out, body)
	}
	return out
}

// assertNoLeak fails for every needle or trace ID found in haystack, in any
// case, and for trace IDs also as base64 of their bytes.
func assertNoLeak(t *testing.T, c leakCorpus, where, haystack string) {
	t.Helper()
	lower := strings.ToLower(haystack)
	for _, m := range c.Markers {
		if strings.Contains(lower, strings.ToLower(m.Needle)) {
			i := strings.Index(lower, strings.ToLower(m.Needle))
			t.Errorf("%s: the %s marker %q leaked: …%s…", where, m.Kind, m.Needle, haystack[max(0, i-80):min(len(haystack), i+80)])
		}
	}
	for _, id := range c.TraceIDs {
		b, _ := hex.DecodeString(id)
		for _, form := range []string{strings.ToLower(id), base64.StdEncoding.EncodeToString(b), base64.RawURLEncoding.EncodeToString(b)} {
			if strings.Contains(lower, strings.ToLower(form)) {
				t.Errorf("%s: raw trace ID %s leaked (as %s)", where, id, form)
			}
		}
	}
}

// decodedEnvelopes adds a batch file's DSSE payload, decoded, to its text.
func decodedEnvelopes(t *testing.T, raw []byte) string {
	t.Helper()
	var file struct {
		Envelope struct {
			Payload string `json:"payload"`
		} `json:"envelope"`
	}
	out := string(raw)
	if err := json.Unmarshal(raw, &file); err != nil || file.Envelope.Payload == "" {
		t.Fatalf("a batch file without an envelope: %v %.200s", err, raw)
	}
	{
		p, err := base64.StdEncoding.DecodeString(file.Envelope.Payload)
		if err != nil {
			t.Fatalf("envelope payload: %v", err)
		}
		out += "\n" + string(p)
	}
	return out
}

// Acceptance (P8-3): spans stuffed with prompts, personal data and
// credentials in every position and attribute spelling leave nothing of them,
// and no raw trace ID, in the dry-run output, the agent's logs, the receiver's
// answers, the signed batches, the dashboard API, report, check or the AI-BOM.
func TestNothingOfTheLeakCorpusLeavesTheAgent(t *testing.T) {
	c := loadLeakCorpus(t)
	exports := leakExports(c, time.Now().UTC().Add(-2*time.Minute))
	// The span with content, and each fact span but the one whose
	// gen_ai.operation.name the marker replaces, is an LLM call.
	llmSpans := len(c.Markers) * len(c.FactAttributes)

	// 1. Dry run over the corpus: what would be sent, decoded.
	replay := filepath.Join(t.TempDir(), "leak.jsonl")
	var lines []byte
	for _, e := range exports {
		lines = append(append(lines, e...), '\n')
	}
	if err := os.WriteFile(replay, lines, 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	env := getenv("TELLPROOF_SALT_SECRET", "a-salt-secret-0123456", "TELLPROOF_DATA_DIR", filepath.Join(t.TempDir(), "d"))
	if code := run(context.Background(), []string{"--config", example, "--dry-run", "--replay", replay}, env, &out, &errb); code != 0 {
		t.Fatalf("dry run: exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), `"invalid"`) || !strings.Contains(out.String(), "content_present_spans") {
		t.Errorf("dry run shows no invalid identifiers or no content count:\n%.500s", out.String())
	}
	assertNoLeak(t, c, "dry-run output", out.String())
	assertNoLeak(t, c, "dry-run log", errb.String())

	// 2. Local mode: the receiver, the signed log and everything that reads it.
	tickEvery = 100 * time.Millisecond
	defer func() { tickEvery = time.Minute }()
	t.Chdir(t.TempDir())
	listen, dash := freePort(t), freePort(t)
	dataDir, _ := filepath.Abs("data")
	cfg := filepath.Join(t.TempDir(), "agent.json")
	_ = os.WriteFile(cfg, []byte(`{"otlp_listen": "`+listen+`", "dashboard_listen": "`+dash+`", "aggregation": {"grace_minutes": 5, "first_batch_seconds": 1}}`), 0o600)
	lenv := getenv("TELLPROOF_DATA_DIR", dataDir)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	logs := &lockedBuffer{}
	go func() { done <- run(ctx, []string{"run", "--local", "--config", cfg}, lenv, logs, logs) }()
	defer func() { cancel(); <-done }()
	waitFor(t, listen, logs)

	var answers strings.Builder
	post := func(body []byte, gz bool) int {
		req, _ := http.NewRequest(http.MethodPost, "http://"+listen+"/v1/traces", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if gz {
			var zb bytes.Buffer
			w := gzip.NewWriter(&zb)
			_, _ = w.Write(body)
			_ = w.Close()
			req.Body, req.ContentLength = io.NopCloser(&zb), int64(zb.Len())
			req.Header.Set("Content-Encoding", "gzip")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		answers.Write(b)
		return resp.StatusCode
	}
	for i, e := range exports {
		if code := post(e, i%2 == 1); code != http.StatusOK {
			t.Fatalf("export %d: HTTP %d: %s", i, code, answers.String())
		}
	}
	// Bodies the receiver refuses must not be quoted back either.
	for _, m := range c.Markers[:3] {
		post([]byte(`{"resourceSpans": [{"scopeSpans": [{"spans": [{"name": "`+m.Text), false)
		post([]byte(`{"resourceSpans": []} {"trailing": "`+m.Text+`"}`), false)
	}

	// Wait until a batch holds every LLM span.
	var batches string
	for i := 0; ; i++ {
		files, _ := filepath.Glob(filepath.Join(dataDir, "local", "*.batch.json"))
		batches = ""
		for _, f := range files {
			b, _ := os.ReadFile(f)
			batches += decodedEnvelopes(t, b) + "\n"
		}
		if strings.Contains(batches, fmt.Sprintf(`"llm_calls":%d`, llmSpans)) {
			break
		}
		if i == 200 {
			t.Fatalf("no batch with all %d LLM calls: %s\n%s", llmSpans, logs, batches)
		}
		time.Sleep(50 * time.Millisecond)
	}
	assertNoLeak(t, c, "receiver answers", answers.String())
	assertNoLeak(t, c, "signed batches", batches)

	get := func(path string) string {
		resp, err := http.Get("http://" + dash + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK || !json.Valid(b) {
			t.Fatalf("%s: HTTP %d %.300s", path, resp.StatusCode, b)
		}
		return string(b)
	}
	if code := run(context.Background(), []string{"policy", "init", "--data-dir", dataDir}, lenv, io.Discard, io.Discard); code != 0 {
		t.Fatalf("policy init: exit %d", code)
	}
	assertNoLeak(t, c, "policy init", mustRead(t, "tellproof.policy.json"))
	apiLog := get("/api/log")
	if !strings.Contains(apiLog, `"invalid"`) {
		t.Errorf("the dashboard shows no invalid identifiers: %.500s", apiLog)
	}
	assertNoLeak(t, c, "dashboard /api/log", apiLog)
	assertNoLeak(t, c, "dashboard /api/check", get("/api/check"))
	for _, args := range [][]string{{"report", "--json"}, {"report"}, {"check", "--json"}, {"check"}, {"check", "--self-test", "--json"}, {"aibom"}, {"doctor", "--json"}} {
		var o bytes.Buffer
		run(context.Background(), append(args, "--data-dir", dataDir), lenv, &o, &o)
		assertNoLeak(t, c, strings.Join(args, " "), o.String())
	}
	assertNoLeak(t, c, "agent log", logs.String())
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
