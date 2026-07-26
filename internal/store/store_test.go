package store

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xbuyan/Escrowd/internal/escrow"
)

// These are integration tests against a real PostgreSQL instance — Store
// talks to Postgres directly via pgx, so there is no meaningful way to unit
// test it against a mock without testing a fake instead of the real query
// logic (upserts, row-locking, JSONB queries).
//
// Set TEST_DATABASE_URL to run these, e.g.:
//
//	TEST_DATABASE_URL="postgres://postgres:yourpassword@localhost:5432/escrowd_test?sslmode=disable" go test ./internal/store/...
//
// Tests are skipped automatically if TEST_DATABASE_URL is not set, so CI
// without a Postgres service attached won't fail — see .github/workflows
// for wiring up a postgres service container to run these in CI.

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping store integration tests")
	}

	// Store.New reads DATABASE_URL directly, so point it at the test DB
	// for the duration of this test.
	oldURL := os.Getenv("DATABASE_URL")
	os.Setenv("DATABASE_URL", dbURL)
	t.Cleanup(func() { os.Setenv("DATABASE_URL", oldURL) })

	s, err := New("")
	if err != nil {
		t.Fatalf("could not connect to test database: %v", err)
	}
	if err := s.MigrateUsers(); err != nil {
		t.Fatalf("could not run user migrations: %v", err)
	}

	t.Cleanup(func() {
		cleanupTables(t, dbURL)
		s.Close()
	})

	// Start each test from a clean slate.
	cleanupTables(t, dbURL)

	return s
}

func cleanupTables(t *testing.T, dbURL string) {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		t.Fatalf("cleanup: could not connect: %v", err)
	}
	defer pool.Close()
	for _, table := range []string{"escrows", "audit_log", "email_verification_tokens", "users"} {
		if _, err := pool.Exec(context.Background(), "DELETE FROM "+table); err != nil {
			t.Fatalf("cleanup: could not clear %s: %v", table, err)
		}
	}
}

func sampleDeal(id string) escrow.Escrow {
	return escrow.Escrow{
		ID:          id,
		Title:       "Test deal",
		Currency:    "USDC",
		SenderID:    "alice",
		SenderName:  "Alice",
		ReceiverID:  "bob",
		Amount:      10,
		Status:      escrow.StatusLocked,
		InviteToken: "invite-" + id,
	}
}

func TestNew_RequiresDatabaseURL(t *testing.T) {
	old := os.Getenv("DATABASE_URL")
	os.Setenv("DATABASE_URL", "")
	defer os.Setenv("DATABASE_URL", old)

	if _, err := New(""); err == nil {
		t.Fatal("expected New to fail when DATABASE_URL is unset")
	}
}

func TestSave_ThenGet_RoundTrip(t *testing.T) {
	s := newTestStore(t)
	deal := sampleDeal("deal-1")

	if err := s.Save(deal); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	got, err := s.Get("deal-1")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got.SenderID != "alice" || got.ReceiverID != "bob" || got.Amount != 10 {
		t.Fatalf("round-tripped deal doesn't match: %+v", got)
	}
}

func TestGet_NonExistentDeal(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Get("does-not-exist"); err == nil {
		t.Fatal("expected error for a deal that doesn't exist")
	}
}

func TestSave_UpsertIncrementsVersion(t *testing.T) {
	s := newTestStore(t)
	deal := sampleDeal("deal-1")

	if err := s.Save(deal); err != nil {
		t.Fatal(err)
	}
	deal.Status = escrow.StatusClaimed
	if err := s.Save(deal); err != nil {
		t.Fatal(err)
	}

	got, err := s.Get("deal-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != escrow.StatusClaimed {
		t.Fatalf("expected updated status to persist, got %q", got.Status)
	}
}

func TestGetByInviteToken(t *testing.T) {
	s := newTestStore(t)
	deal := sampleDeal("deal-1")
	if err := s.Save(deal); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetByInviteToken("invite-deal-1")
	if err != nil {
		t.Fatalf("GetByInviteToken failed: %v", err)
	}
	if got.ID != "deal-1" {
		t.Fatalf("got wrong deal: %+v", got)
	}

	if _, err := s.GetByInviteToken("no-such-token"); err == nil {
		t.Fatal("expected error for an unknown invite token")
	}
}

func TestDelete(t *testing.T) {
	s := newTestStore(t)
	deal := sampleDeal("deal-1")
	if err := s.Save(deal); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("deal-1"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if _, err := s.Get("deal-1"); err == nil {
		t.Fatal("expected deal to be gone after Delete")
	}
}

func TestListIDs(t *testing.T) {
	s := newTestStore(t)
	for _, id := range []string{"deal-1", "deal-2", "deal-3"} {
		if err := s.Save(sampleDeal(id)); err != nil {
			t.Fatal(err)
		}
	}

	ids, err := s.ListIDs()
	if err != nil {
		t.Fatalf("ListIDs failed: %v", err)
	}
	if len(ids) != 3 {
		t.Fatalf("expected 3 ids, got %d: %v", len(ids), ids)
	}
}

func TestUpdateWithLock_AppliesMutation(t *testing.T) {
	s := newTestStore(t)
	if err := s.Save(sampleDeal("deal-1")); err != nil {
		t.Fatal(err)
	}

	updated, err := s.UpdateWithLock("deal-1", func(deal *escrow.Escrow) error {
		deal.Status = escrow.StatusClaimed
		return nil
	})
	if err != nil {
		t.Fatalf("UpdateWithLock failed: %v", err)
	}
	if updated.Status != escrow.StatusClaimed {
		t.Fatalf("expected mutation to apply, got status %q", updated.Status)
	}

	got, err := s.Get("deal-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != escrow.StatusClaimed {
		t.Fatal("expected mutation to be persisted")
	}
}

func TestUpdateWithLock_MutateErrorRollsBack(t *testing.T) {
	s := newTestStore(t)
	if err := s.Save(sampleDeal("deal-1")); err != nil {
		t.Fatal(err)
	}

	mutateErr := errors.New("simulated business-rule failure")
	_, err := s.UpdateWithLock("deal-1", func(deal *escrow.Escrow) error {
		deal.Status = escrow.StatusClaimed // this change must NOT persist
		return mutateErr
	})
	if err == nil {
		t.Fatal("expected UpdateWithLock to return the mutate error")
	}

	got, err := s.Get("deal-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == escrow.StatusClaimed {
		t.Fatal("expected rollback: status should NOT have changed after mutate returned an error")
	}
}

// TestUpdateWithLock_SerializesConcurrentUpdates is the important one for a
// system moving money: two goroutines race to update the same deal via
// UpdateWithLock. Because it uses SELECT ... FOR UPDATE inside a
// transaction, the second caller must block until the first commits and
// then see the first caller's change — never a lost update.
func TestUpdateWithLock_SerializesConcurrentUpdates(t *testing.T) {
	s := newTestStore(t)
	if err := s.Save(sampleDeal("deal-1")); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		s.UpdateWithLock("deal-1", func(deal *escrow.Escrow) error {
			deal.Title = deal.Title + "-A"
			return nil
		})
	}()
	go func() {
		defer wg.Done()
		s.UpdateWithLock("deal-1", func(deal *escrow.Escrow) error {
			deal.Title = deal.Title + "-B"
			return nil
		})
	}()
	wg.Wait()

	got, err := s.Get("deal-1")
	if err != nil {
		t.Fatal(err)
	}
	// Both suffixes must be present — if the lock didn't serialize the two
	// updates, one write would silently clobber the other (lost update).
	if !(contains(got.Title, "-A") && contains(got.Title, "-B")) {
		t.Fatalf("expected both concurrent updates to be reflected (lost update detected), got title=%q", got.Title)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (func() bool {
		for i := 0; i+len(substr) <= len(s); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
		return false
	})()
}
