package store

import (
	"errors"
	"strings"
	"supportflow/internal/billing"
	"supportflow/internal/ticket"
	"sync"
	"testing"
)

func TestBillingConcurrentAdmissionAndRestart(t *testing.T) {
	s := testStore(t, 10)
	ctx := t.Context()
	policy := billing.Policy{Daily: 100, PerTicket: 100, InputRate: 1, OutputRate: 1}
	var wg sync.WaitGroup
	results := make(chan error, 20)
	for range 20 {
		wg.Go(func() {
			_, err := s.ReserveAI(ctx, policy, billing.Request{TicketID: "ticket", Attempt: 1, Model: "test", Reserve: 20, InputRate: 1, OutputRate: 1})
			results <- err
		})
	}
	wg.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		} else if !errors.Is(err, billing.ErrBudget) {
			t.Fatal(err)
		}
	}
	if accepted != 5 {
		t.Fatal(accepted)
	}
	restarted := New(s.pool, 10)
	if _, err := restarted.ReserveAI(ctx, policy, billing.Request{TicketID: "new", Model: "test", Reserve: 1, InputRate: 1, OutputRate: 1}); !errors.Is(err, billing.ErrBudget) {
		t.Fatal("restart reset limit", err)
	}
}

func TestBillingUnknownHoldsAcrossDaysAndPerTicket(t *testing.T) {
	s := testStore(t, 10)
	ctx := t.Context()
	p := billing.Policy{Daily: 100, PerTicket: 30, InputRate: 1, OutputRate: 1}
	request := billing.Request{TicketID: "ticket", Model: "test", Reserve: 25, InputRate: 1, OutputRate: 1}
	id, err := s.ReserveAI(ctx, p, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := billing.Finish(s, id, "provider-id", ticket.Usage{Unknown: true}); err != nil {
		t.Fatal(err)
	}
	_, err = s.pool.Exec(ctx, `UPDATE ai_calls SET created_at=clock_timestamp()-interval '2 days' WHERE id=$1`, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReserveAI(ctx, p, request); !errors.Is(err, billing.ErrBudget) {
		t.Fatal("ticket hold ignored", err)
	}
	request.TicketID = "other"
	request.Reserve = 80
	if _, err = s.ReserveAI(ctx, p, request); !errors.Is(err, billing.ErrBudget) {
		t.Fatal("old unknown hold ignored", err)
	}
	var state, requestID string
	if err = s.pool.QueryRow(ctx, `SELECT state,request_id FROM ai_calls WHERE id=$1`, id).Scan(&state, &requestID); err != nil || state != "unknown" || requestID != "provider-id" {
		t.Fatal(state, requestID, err)
	}
}

func TestBillingKnownZeroSettlesAndCrashStaysPending(t *testing.T) {
	s := testStore(t, 10)
	ctx := t.Context()
	p := billing.Policy{Daily: 100, PerTicket: 100, InputRate: 1, OutputRate: 1}
	r := billing.Request{TicketID: "ticket", Model: "test", Reserve: 40, InputRate: 1, OutputRate: 1}
	id, err := s.ReserveAI(ctx, p, r)
	if err != nil {
		t.Fatal(err)
	}
	if err = billing.Finish(s, id, "request-id", ticket.Usage{}); err != nil {
		t.Fatal(err)
	}
	r.Reserve = 100
	if _, err = s.ReserveAI(ctx, p, r); err != nil {
		t.Fatal("known zero did not release reserve", err)
	}
	if _, err = s.ReserveAI(ctx, p, r); !errors.Is(err, billing.ErrBudget) {
		t.Fatal("crash pending reserve lost", err)
	}
	stats, err := s.AIStats(ctx)
	if err != nil || !strings.Contains(stats, "supportflow_ai_calls_pending 1") || !strings.Contains(stats, "supportflow_ai_calls_recorded 1") {
		t.Fatal(stats, err)
	}
}
