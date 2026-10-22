package store

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"supportflow/internal/ticket"
)

func testStore(t *testing.T, maxQueued int) *Store {
	t.Helper()
	s := testDatabase(t, maxQueued)
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	return s
}

func testDatabase(t *testing.T, maxQueued int) *Store {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := "test_" + rand.Text()
	quoted := pgx.Identifier{name}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanup, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = quoted
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return New(pool, maxQueued)
}

func validAnalysis() ticket.Analysis {
	return ticket.Analysis{Category: "billing", Summary: "Не появился доступ после оплаты", Priority: "medium", PriorityReason: "Проблема одного клиента", DraftReply: "Здравствуйте! Уточните дату платежа."}
}

func TestDurableWorkflowAndReview(t *testing.T) {
	s := testStore(t, 10)
	ctx := t.Context()
	input := ticket.Input{Title: "Оплата", Message: "Деньги списались, доступа нет"}
	created, fresh, err := s.Create(ctx, input, "same-key")
	if err != nil || !fresh {
		t.Fatalf("create: %v fresh=%v", err, fresh)
	}
	replayed, fresh, err := s.Create(ctx, input, "same-key")
	if err != nil || fresh || replayed.ID != created.ID {
		t.Fatalf("replay: %+v fresh=%v err=%v", replayed, fresh, err)
	}
	if _, _, err = s.Create(ctx, ticket.Input{Title: "Other", Message: "Other"}, "same-key"); !errors.Is(err, ticket.ErrConflict) {
		t.Fatalf("different payload: %v", err)
	}
	if _, err = s.Review(ctx, created.ID, ticket.Review{Category: "billing", DraftReply: "Ответ"}, 0); !errors.Is(err, ticket.ErrConflict) {
		t.Fatalf("early review: %v", err)
	}
	claimed, err := s.Claim(ctx, 3, time.Minute)
	if err != nil || claimed.ID != created.ID || claimed.Attempts != 1 {
		t.Fatalf("claim: %+v %v", claimed, err)
	}
	if _, err = s.Claim(ctx, 3, time.Minute); !errors.Is(err, ticket.ErrNotFound) {
		t.Fatalf("claimed twice: %v", err)
	}
	u := ticket.Usage{Model: "test-model", InputTokens: 30, OutputTokens: 20}
	if err = s.Complete(ctx, claimed, validAnalysis(), u); err != nil {
		t.Fatal(err)
	}
	review := ticket.Review{Category: "access", DraftReply: "Исправленный ответ", Approved: true}
	done, err := s.Review(ctx, created.ID, review, 0)
	if err != nil || done.Review == nil || *done.Review != review || done.Analysis.Category != "billing" || done.Usage != u {
		t.Fatalf("review: %+v %v", done, err)
	}
	items, err := s.List(ctx, "access", "completed", 10, 0)
	if err != nil || len(items) != 1 || items[0].ID != created.ID {
		t.Fatalf("corrected category filter: %+v %v", items, err)
	}
}

func TestConcurrentAdmissionAndClaim(t *testing.T) {
	s := testStore(t, 1)
	ctx := t.Context()
	var wg sync.WaitGroup
	results := make(chan error, 12)
	for range 12 {
		wg.Go(func() {
			_, _, err := s.Create(ctx, ticket.Input{Title: "Test", Message: "Test"}, rand.Text())
			results <- err
		})
	}
	wg.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		} else if !errors.Is(err, ticket.ErrQueueFull) {
			t.Fatal(err)
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted=%d, want 1", accepted)
	}
	claims := make(chan error, 12)
	for range 12 {
		wg.Go(func() { _, err := s.Claim(ctx, 3, time.Minute); claims <- err })
	}
	wg.Wait()
	close(claims)
	winners := 0
	for err := range claims {
		if err == nil {
			winners++
		} else if !errors.Is(err, ticket.ErrNotFound) {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatalf("claim winners=%d, want 1", winners)
	}
}

func TestExpiredLeaseFencesStaleWorker(t *testing.T) {
	s := testStore(t, 10)
	ctx := t.Context()
	_, _, err := s.Create(ctx, ticket.Input{Title: "Test", Message: "Test"}, "lease")
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.Claim(ctx, 3, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.pool.Exec(ctx, "UPDATE tickets SET lease_until=now()-interval '1 second' WHERE id=$1", first.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.Complete(ctx, first, validAnalysis(), ticket.Usage{}); !errors.Is(err, ticket.ErrConflict) {
		t.Fatalf("expired completion: %v", err)
	}
	second, err := s.Claim(ctx, 3, time.Minute)
	if err != nil || second.Attempts != 2 {
		t.Fatalf("reclaim: %+v %v", second, err)
	}
	if err = s.Complete(ctx, first, validAnalysis(), ticket.Usage{}); !errors.Is(err, ticket.ErrConflict) {
		t.Fatalf("stale completion: %v", err)
	}
	if err = s.Fail(ctx, first, "stale", false, 0, ticket.Usage{}); !errors.Is(err, ticket.ErrConflict) {
		t.Fatalf("stale failure: %v", err)
	}
	if err = s.Complete(ctx, second, validAnalysis(), ticket.Usage{}); err != nil {
		t.Fatal(err)
	}
}

func TestRetriesAreBoundedAndUsageSurvives(t *testing.T) {
	s := testStore(t, 10)
	ctx := t.Context()
	_, _, err := s.Create(ctx, ticket.Input{Title: "Test", Message: "Test"}, "retry")
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.Claim(ctx, 2, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Fail(ctx, first, "provider_timeout", true, 0, ticket.Usage{InputTokens: 5}); err != nil {
		t.Fatal(err)
	}
	second, err := s.Claim(ctx, 2, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.pool.Exec(ctx, "UPDATE tickets SET lease_until=now()-interval '1 second' WHERE id=$1", second.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Claim(ctx, 2, time.Minute); !errors.Is(err, ticket.ErrNotFound) {
		t.Fatalf("attempt limit: %v", err)
	}
	done, err := s.Get(ctx, second.ID)
	if err != nil || done.Status != "failed" || done.Attempts != 2 || done.Usage.InputTokens != 5 {
		t.Fatalf("final: %+v %v", done, err)
	}
}
