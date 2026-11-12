// Package billing reserves configured monetary units before each paid request.
package billing

import (
	"context"
	"errors"
	"time"

	"supportflow/internal/ticket"
)

var ErrBudget = errors.New("AI budget exhausted")
var ErrUnavailable = errors.New("AI accounting unavailable")

// Units are nanounits of a single configured currency. Rates are per token;
// all processes using a database must use the same currency and price policy.
type Policy struct{ Daily, PerTicket, InputRate, OutputRate int64 }

func (p Policy) Validate() error {
	for _, v := range []int64{p.Daily, p.PerTicket} {
		if v <= 0 || v > 1_000_000_000_000_000 {
			return ErrUnavailable
		}
	}
	for _, v := range []int64{p.InputRate, p.OutputRate} {
		if v <= 0 || v > 1_000_000_000 {
			return ErrUnavailable
		}
	}
	return nil
}

type Request struct {
	TicketID, Model                string
	Attempt                        int
	Reserve, InputRate, OutputRate int64
}
type Ledger interface {
	ReserveAI(context.Context, Policy, Request) (string, error)
	FinishAI(context.Context, string, string, ticket.Usage) error
}

type ownerKey struct{}
type Owner struct {
	ID      string
	Attempt int
}

func WithOwner(ctx context.Context, id string, attempt int) context.Context {
	return context.WithValue(ctx, ownerKey{}, Owner{id, attempt})
}
func OwnerFrom(ctx context.Context) Owner { owner, _ := ctx.Value(ownerKey{}).(Owner); return owner }

// Finish must survive caller cancellation. If it fails, the durable pending
// reservation stays charged; retries never assume an uncertain call was free.
func Finish(ledger Ledger, id, requestID string, usage ticket.Usage) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return ledger.FinishAI(ctx, id, requestID, usage)
}
