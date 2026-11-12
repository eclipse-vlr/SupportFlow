package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"supportflow/internal/billing"
	"supportflow/internal/ticket"
	"testing"
	"time"
)

type ledgerProbe struct {
	reserved, finished int
	usage              ticket.Usage
	fail               bool
}

func (p *ledgerProbe) ReserveAI(context.Context, billing.Policy, billing.Request) (string, error) {
	p.reserved++
	if p.fail {
		return "", billing.ErrBudget
	}
	return "local-request", nil
}
func (p *ledgerProbe) FinishAI(ctx context.Context, id, request string, u ticket.Usage) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	p.finished++
	p.usage = u
	return nil
}

func TestMissingUsageKeepsUsefulDraftAndUnknownHold(t *testing.T) {
	for _, usage := range []any{nil, map[string]int64{"input_tokens": 0}, map[string]int64{"input_tokens": 0, "output_tokens": 0}} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "completed", "model": "test", "usage": usage, "output": []any{map[string]any{"type": "message", "content": []any{map[string]string{"type": "output_text", "text": draftAnalysisJSON("Здравствуйте! Уточните дату оплаты.")}}}}})
		}))
		c := New(srv.URL, "key", "test", 100, time.Second)
		p := &ledgerProbe{}
		c.SetBilling(p, billing.Policy{Daily: 100000, PerTicket: 100000, InputRate: 1, OutputRate: 1})
		a, u, err := c.Analyze(billing.WithOwner(t.Context(), "ticket", 1), ticket.Input{})
		m, _ := usage.(map[string]int64)
		_, hasOutput := m["output_tokens"]
		if err != nil || a.DraftReply == "" || u.Unknown == hasOutput || p.usage.Unknown != u.Unknown || p.reserved != 1 || p.finished != 1 {
			t.Fatalf("analysis=%+v usage=%+v probe=%+v err=%v", a, u, p, err)
		}
		c.Close()
		srv.Close()
	}
}

func TestBudgetDenialPreventsTransport(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer srv.Close()
	c := New(srv.URL, "key", "test", 100, time.Second)
	defer c.Close()
	p := &ledgerProbe{fail: true}
	c.SetBilling(p, billing.Policy{Daily: 1, PerTicket: 1, InputRate: 1, OutputRate: 1})
	_, _, err := c.Analyze(billing.WithOwner(t.Context(), "ticket", 1), ticket.Input{})
	var e *Error
	if !errors.As(err, &e) || e.Code != "ai_budget_exhausted" || calls != 0 || p.finished != 0 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
