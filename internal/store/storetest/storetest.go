// Package storetest gives tests a freshly migrated Postgres database.
//
// Set PLUMBLINE_TEST_DATABASE_URL to an owner (superuser) URL of a server the
// tests may create databases on; CI's Postgres service is one. Without it the
// tests skip, unless PLUMBLINE_REQUIRE_DB=1 (set in CI), when they fail.
package storetest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/lumetrix-advanced-technologies/plumbline/internal/store"
	"github.com/lumetrix-advanced-technologies/plumbline/migrations"
)

// ServerURL is the owner URL from the environment, or skips the test.
func ServerURL(t testing.TB) string {
	t.Helper()
	u := os.Getenv("PLUMBLINE_TEST_DATABASE_URL")
	if u == "" {
		if os.Getenv("PLUMBLINE_REQUIRE_DB") == "1" {
			t.Fatal("PLUMBLINE_TEST_DATABASE_URL is not set")
		}
		t.Skip("PLUMBLINE_TEST_DATABASE_URL is not set")
	}
	return u
}

// EmptyDB creates an empty database and returns its owner URL; it is dropped when the test ends.
func EmptyDB(t testing.TB) string {
	t.Helper()
	server := ServerURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, server)
	if err != nil {
		t.Fatalf("storetest: connect: %v", err)
	}
	defer conn.Close(ctx)
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	name := "plumbline_t_" + hex.EncodeToString(b)
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("storetest: create database: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if c, err := pgx.Connect(ctx, server); err == nil {
			_, _ = c.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
			c.Close(ctx)
		}
	})
	u, err := url.Parse(server)
	if err != nil {
		t.Fatal("storetest: PLUMBLINE_TEST_DATABASE_URL does not parse")
	}
	u.Path = "/" + name
	return u.String()
}

// MigratedDB is EmptyDB with every migration applied.
func MigratedDB(t testing.TB) string {
	t.Helper()
	dbURL := EmptyDB(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	ms, err := store.LoadMigrations(migrations.Files)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Migrate(ctx, conn, ms); err != nil {
		t.Fatal(err)
	}
	return dbURL
}

// App opens a store on dbURL as plumbline_app (the owner assumes the role).
func App(t testing.TB, dbURL string) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), dbURL, store.Options{Role: "plumbline_app"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}
