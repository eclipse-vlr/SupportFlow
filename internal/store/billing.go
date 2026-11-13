package store

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"supportflow/internal/billing"
	"supportflow/internal/ticket"
)

func rollbackAI(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}

// ReserveAI serializes global admission, including rewrites and retry attempts.
// Unknown/pending calls from previous days still hold funds until reconciliation.
func (s *Store) ReserveAI(ctx context.Context, policy billing.Policy, request billing.Request) (string, error) {
	if policy.Validate() != nil || request.Reserve <= 0 || request.Reserve > 1_000_000_000_000_000 || request.TicketID == "" || request.InputRate != policy.InputRate || request.OutputRate != policy.OutputRate {
		return "", billing.ErrUnavailable
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer rollbackAI(tx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(78345903)`); err != nil {
		return "", err
	}
	// First writer fixes the currency/rates. Limits may be lowered immediately;
	// raising limits or changing rates requires an explicit database policy change.
	if _, err = tx.Exec(ctx, `INSERT INTO ai_budget_policy(singleton,daily,per_ticket,input_rate,output_rate) VALUES(true,$1,$2,$3,$4) ON CONFLICT(singleton) DO UPDATE SET daily=LEAST(ai_budget_policy.daily,EXCLUDED.daily),per_ticket=LEAST(ai_budget_policy.per_ticket,EXCLUDED.per_ticket)`, policy.Daily, policy.PerTicket, policy.InputRate, policy.OutputRate); err != nil {
		return "", err
	}
	var stored billing.Policy
	if err = tx.QueryRow(ctx, `SELECT daily,per_ticket,input_rate,output_rate FROM ai_budget_policy WHERE singleton`).Scan(&stored.Daily, &stored.PerTicket, &stored.InputRate, &stored.OutputRate); err != nil {
		return "", err
	}
	if stored.InputRate != policy.InputRate || stored.OutputRate != policy.OutputRate {
		return "", billing.ErrUnavailable
	}
	var daily, perTicket int64
	var overrun bool
	err = tx.QueryRow(ctx, `SELECT COALESCE(sum(CASE WHEN state='recorded' THEN cost ELSE reserved END) FILTER(WHERE state<>'recorded' OR created_at >= date_trunc('day',clock_timestamp() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'),0), COALESCE(sum(CASE WHEN state='recorded' THEN cost ELSE reserved END) FILTER(WHERE ticket_id=$1),0),COALESCE(bool_or(cost>reserved),false) FROM ai_calls`, request.TicketID).Scan(&daily, &perTicket, &overrun)
	if err != nil {
		return "", err
	}
	if overrun || daily > stored.Daily-request.Reserve || perTicket > stored.PerTicket-request.Reserve {
		if err = tx.Commit(ctx); err != nil {
			return "", err
		}
		return "", billing.ErrBudget
	}
	id := rand.Text()
	_, err = tx.Exec(ctx, `INSERT INTO ai_calls(id,ticket_id,attempt,model,reserved,input_rate,output_rate,prompt_version) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, id, request.TicketID, request.Attempt, request.Model, request.Reserve, request.InputRate, request.OutputRate, request.PromptVersion)
	if err != nil {
		return "", err
	}
	return id, tx.Commit(ctx)
}

func (s *Store) FinishAI(ctx context.Context, id, requestID string, u ticket.Usage) error {
	if len(requestID) > 200 {
		requestID = ""
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollbackAI(tx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(78345903)`); err != nil {
		return err
	}
	var state string
	var inRate, outRate int64
	err = tx.QueryRow(ctx, `SELECT state,input_rate,output_rate FROM ai_calls WHERE id=$1 FOR UPDATE`, id).Scan(&state, &inRate, &outRate)
	if errors.Is(err, pgx.ErrNoRows) {
		return billing.ErrUnavailable
	}
	if err != nil {
		return err
	}
	if state != "pending" {
		return nil
	}
	if u.Unknown || u.InputTokens < 0 || u.OutputTokens < 0 || u.InputTokens > 1_000_000 || u.OutputTokens > 1_000_000 {
		_, err = tx.Exec(ctx, `UPDATE ai_calls SET state='unknown',request_id=$2,finished_at=clock_timestamp(),duration_ms=(EXTRACT(epoch FROM clock_timestamp()-created_at)*1000)::bigint WHERE id=$1`, id, requestID)
	} else {
		_, err = tx.Exec(ctx, `UPDATE ai_calls SET state='recorded',request_id=$2,input_tokens=$3,output_tokens=$4,cost=$5,finished_at=clock_timestamp(),duration_ms=(EXTRACT(epoch FROM clock_timestamp()-created_at)*1000)::bigint WHERE id=$1`, id, requestID, u.InputTokens, u.OutputTokens, u.InputTokens*inRate+u.OutputTokens*outRate)
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) AIStats(ctx context.Context) (string, error) {
	var pending, unknown, recorded, duration, rewrites, queued, age, retries, errorsCount, edited int64
	err := s.pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE state='pending'),count(*) FILTER(WHERE state='unknown'),count(*) FILTER(WHERE state='recorded'),COALESCE(sum(duration_ms),0) FROM ai_calls`).Scan(&pending, &unknown, &recorded, &duration)
	if err != nil {
		return "", err
	}
	if err = s.pool.QueryRow(ctx, `SELECT COALESCE(sum(n-1),0) FROM (SELECT count(*) n FROM ai_calls GROUP BY ticket_id,attempt) calls`).Scan(&rewrites); err != nil {
		return "", err
	}
	err = s.pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE status='pending'),COALESCE(max(EXTRACT(epoch FROM clock_timestamp()-created_at)) FILTER(WHERE status='pending'),0)::bigint,COALESCE(sum(GREATEST(attempts-1,0)),0),count(*) FILTER(WHERE last_error<>''),count(*) FILTER(WHERE feedback->>'rating'='rewritten') FROM tickets`).Scan(&queued, &age, &retries, &errorsCount, &edited)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("supportflow_ai_calls_pending %d\nsupportflow_ai_calls_unknown %d\nsupportflow_ai_calls_recorded %d\nsupportflow_ai_provider_duration_milliseconds_sum %d\nsupportflow_ai_rewrites %d\nsupportflow_queue_pending %d\nsupportflow_queue_oldest_seconds %d\nsupportflow_queue_retries %d\nsupportflow_analysis_errors %d\nsupportflow_operator_rewritten %d\n", pending, unknown, recorded, duration, rewrites, queued, age, retries, errorsCount, edited), nil
}
