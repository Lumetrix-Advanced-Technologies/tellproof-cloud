package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/lumetrix-advanced-technologies/plumbline/internal/store/storetest"
)

func env(kv ...string) func(string) string {
	m := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return func(k string) string { return m[k] }
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"frobnicate"}, {"migrate", "extra"}, {"migrate"}} {
		var out, errb bytes.Buffer
		if code := run(context.Background(), args, env(), &out, &errb); code != 2 {
			t.Errorf("%v: exit %d", args, code)
		}
	}
	var out bytes.Buffer
	if run(context.Background(), []string{"version"}, env(), &out, &out) != 0 || !strings.HasPrefix(out.String(), "plumbline-server ") {
		t.Errorf("version: %q", out.String())
	}
}

func TestMigrateCommand(t *testing.T) {
	db := storetest.EmptyDB(t)
	for i, want := range []string{`"applied":1`, `"applied":0`} {
		var out, errb bytes.Buffer
		if code := run(context.Background(), []string{"migrate"}, env("DATABASE_URL_OWNER", db), &out, &errb); code != 0 {
			t.Fatalf("run %d: exit %d: %s", i, code, errb.String())
		}
		if !strings.Contains(errb.String(), want) {
			t.Errorf("run %d log: %s", i, errb.String())
		}
	}
	var out, errb bytes.Buffer
	if code := run(context.Background(), []string{"migrate"}, env("DATABASE_URL_OWNER", "postgres://u:hunter2@127.0.0.1:1/x"), &out, &errb); code != 1 || strings.Contains(errb.String(), "hunter2") {
		t.Errorf("bad URL: exit %d, %s", code, errb.String())
	}
}
