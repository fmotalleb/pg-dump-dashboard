package session_test

import (
	"context"
	"testing"
	"time"

	"github.com/fmotalleb/pg-dump-dashboard/internal/execution"
	"github.com/fmotalleb/pg-dump-dashboard/internal/postgres"
	"github.com/fmotalleb/pg-dump-dashboard/internal/session"
)

func TestStoreCreateGetDelete(t *testing.T) {
	t.Parallel()

	store := session.NewStore(time.Minute)
	creds := session.Credentials{User: "reporter", Password: execution.Secret("secret"), Database: "postgres"}

	sess := store.Create(creds)
	if sess.ID == "" {
		t.Fatal("session id must not be empty")
	}
	if store.Len() != 1 {
		t.Fatalf("store length = %d", store.Len())
	}

	got, ok := store.Get(sess.ID)
	if !ok {
		t.Fatal("session not found")
	}
	if got.Creds.User != "reporter" || got.Creds.Password.Reveal() != "secret" {
		t.Fatalf("unexpected credentials: %+v", got.Creds.User)
	}

	store.Delete(sess.ID)
	if _, ok := store.Get(sess.ID); ok {
		t.Fatal("deleted session is still reachable")
	}
	if store.Len() != 0 {
		t.Fatalf("store length = %d", store.Len())
	}
}

func TestStoreRejectsUnknownOrEmptyID(t *testing.T) {
	t.Parallel()

	store := session.NewStore(time.Minute)
	if _, ok := store.Get(""); ok {
		t.Fatal("empty id must not resolve")
	}
	if _, ok := store.Get("unknown"); ok {
		t.Fatal("unknown id must not resolve")
	}
}

func TestStoreExpiresIdleSessions(t *testing.T) {
	t.Parallel()

	store := session.NewStore(40 * time.Millisecond)
	sess := store.Create(session.Credentials{User: "reporter"})

	time.Sleep(80 * time.Millisecond)

	if _, ok := store.Get(sess.ID); ok {
		t.Fatal("idle session should have expired")
	}
	if store.Len() != 0 {
		t.Fatalf("expired session was not removed: %d", store.Len())
	}
}

func TestGCRemovesExpiredSessions(t *testing.T) {
	t.Parallel()

	store := session.NewStore(30 * time.Millisecond)
	store.Create(session.Credentials{User: "a"})
	store.Create(session.Credentials{User: "b"})
	time.Sleep(60 * time.Millisecond)

	if removed := store.GC(); removed != 2 {
		t.Fatalf("GC removed %d sessions, want 2", removed)
	}
}

func TestSessionCachesOverview(t *testing.T) {
	t.Parallel()

	store := session.NewStore(time.Minute)
	sess := store.Create(session.Credentials{User: "reporter"})

	if sess.Overview() != nil {
		t.Fatal("new session must not have an overview")
	}

	overview := &postgres.Overview{Databases: []postgres.Database{{Name: "appdb"}, {Name: "other"}}}
	sess.SetOverview(overview)

	if sess.Overview() != overview {
		t.Fatal("overview was not stored")
	}
	if db, ok := sess.Database("other"); !ok || db.Name != "other" {
		t.Fatalf("lookup failed: %+v %v", db, ok)
	}
	if _, ok := sess.Database("missing"); ok {
		t.Fatal("unknown database must not resolve")
	}
}

func TestRunGCStopsWithContext(t *testing.T) {
	t.Parallel()

	store := session.NewStore(time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		store.RunGC(ctx, 5*time.Millisecond)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunGC did not stop after cancellation")
	}
}

func TestDefaultTTL(t *testing.T) {
	t.Parallel()

	store := session.NewStore(0)
	if store.TTL() != session.DefaultTTL {
		t.Fatalf("ttl = %v, want %v", store.TTL(), session.DefaultTTL)
	}
}
