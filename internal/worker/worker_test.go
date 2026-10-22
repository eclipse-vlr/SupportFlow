package worker

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"supportflow/internal/llm"
	"supportflow/internal/ticket"
)

type testQueue struct {
	t                     ticket.Ticket
	claimed               bool
	complete, fail, retry bool
	code                  string
	usage                 ticket.Usage
}

func (q *testQueue) Claim(context.Context, int, time.Duration) (ticket.Ticket, error) {
	if q.claimed {
		return ticket.Ticket{}, ticket.ErrNotFound
	}
	q.claimed = true
	return q.t, nil
}
func (q *testQueue) Complete(_ context.Context, _ ticket.Ticket, _ ticket.Analysis, u ticket.Usage) error {
	q.complete = true
	q.usage = u
	return nil
}
func (q *testQueue) Fail(_ context.Context, _ ticket.Ticket, code string, retry bool, _ time.Duration, u ticket.Usage) error {
	q.fail = true
	q.retry = retry
	q.code = code
	q.usage = u
	return nil
}

type analyzerFunc func(context.Context, ticket.Input) (ticket.Analysis, ticket.Usage, error)

func (f analyzerFunc) Analyze(ctx context.Context, i ticket.Input) (ticket.Analysis, ticket.Usage, error) {
	return f(ctx, i)
}

func TestRetryPolicy(t *testing.T) {
	for _, tc := range []struct {
		name      string
		attempt   int
		err       error
		wantRetry bool
	}{
		{"rate limited", 1, &llm.Error{Code: "provider_http_429", Retryable: true}, true},
		{"auth permanent", 1, &llm.Error{Code: "provider_http_401"}, false},
		{"exhausted", 3, &llm.Error{Code: "provider_http_503", Retryable: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := &testQueue{}
			u := ticket.Usage{InputTokens: 10}
			w := Worker{Queue: q, Analyzer: analyzerFunc(func(context.Context, ticket.Input) (ticket.Analysis, ticket.Usage, error) {
				return ticket.Analysis{}, u, tc.err
			}), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Timeout: time.Second, MaxAttempts: 3}
			w.process(t.Context(), ticket.Ticket{Attempts: tc.attempt})
			if !q.fail || q.retry != tc.wantRetry || q.complete || q.usage != u {
				t.Fatalf("result: %+v", q)
			}
		})
	}
}

func TestShutdownJoinsBlockedAnalyzer(t *testing.T) {
	started := make(chan struct{})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	q := &testQueue{t: ticket.Ticket{Attempts: 1}}
	w := Worker{Queue: q, Analyzer: analyzerFunc(func(ctx context.Context, _ ticket.Input) (ticket.Analysis, ticket.Usage, error) {
		close(started)
		<-ctx.Done()
		return ticket.Analysis{}, ticket.Usage{}, ctx.Err()
	}), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Timeout: time.Minute, MaxAttempts: 3}
	done := make(chan struct{})
	go func() { defer close(done); w.Run(ctx, 1) }()
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not join on shutdown")
	}
	if q.complete || q.fail {
		t.Fatal("shutdown must leave lease for recovery")
	}
}
