package worker

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"supportflow/internal/llm"
	"supportflow/internal/ticket"
)

type Queue interface {
	Claim(context.Context, int, time.Duration) (ticket.Ticket, error)
	Complete(context.Context, ticket.Ticket, ticket.Analysis, ticket.Usage) error
	Fail(context.Context, ticket.Ticket, string, bool, time.Duration, ticket.Usage) error
}

type Worker struct {
	Queue       Queue
	Analyzer    llm.Analyzer
	Log         *slog.Logger
	Timeout     time.Duration
	MaxAttempts int
}

// Run owns and joins a fixed number of workers. Only the process context owns them.
func (w *Worker) Run(ctx context.Context, count int) {
	var group sync.WaitGroup
	for range count {
		group.Go(func() { w.loop(ctx) })
	}
	group.Wait()
}

func (w *Worker) loop(ctx context.Context) {
	for ctx.Err() == nil {
		claimCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		t, err := w.Queue.Claim(claimCtx, w.MaxAttempts, w.Timeout+15*time.Second)
		cancel()
		if err != nil {
			if !errors.Is(err, ticket.ErrNotFound) && ctx.Err() == nil {
				w.Log.Error("queue claim failed")
			}
			if !wait(ctx, time.Second) {
				return
			}
			continue
		}
		w.process(ctx, t)
	}
}

func (w *Worker) process(ctx context.Context, t ticket.Ticket) {
	callCtx, cancel := context.WithTimeout(ctx, w.Timeout)
	a, usage, err := w.Analyzer.Analyze(callCtx, t.Input)
	cancel()
	// On process cancellation leave the durable lease for recovery, without
	// calling the provider again or claiming that the remote request was free.
	if ctx.Err() != nil {
		return
	}
	writeCtx, writeCancel := context.WithTimeout(ctx, 5*time.Second)
	defer writeCancel()
	if err == nil {
		if validationErr := a.Validate(); validationErr != nil {
			err = &llm.Error{Code: "provider_invalid_analysis"}
		}
	}
	if err == nil {
		if err = w.Queue.Complete(writeCtx, t, a, usage); err != nil {
			w.Log.Error("ticket completion failed", "ticket_id", t.ID)
		}
		return
	}
	code, retry, delay := "provider_failed", false, time.Duration(0)
	var providerErr *llm.Error
	if errors.As(err, &providerErr) {
		code = providerErr.Code
		retry = providerErr.Retryable && t.Attempts < w.MaxAttempts
		delay = max(providerErr.RetryAfter, time.Duration(1<<min(t.Attempts, 5))*time.Second+time.Duration(rand.IntN(1000))*time.Millisecond)
	}
	if err = w.Queue.Fail(writeCtx, t, code, retry, delay, usage); err != nil {
		w.Log.Error("ticket failure persistence failed", "ticket_id", t.ID)
		return
	}
	w.Log.Warn("ticket analysis failed", "ticket_id", t.ID, "code", code, "retry", retry)
}

func wait(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
