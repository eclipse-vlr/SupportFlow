package main

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"supportflow/internal/llm"
	"supportflow/internal/ticket"
)

type evaluationFake struct{}

func (evaluationFake) Analyze(_ context.Context, input ticket.Input) (ticket.Analysis, ticket.Usage, error) {
	usage := ticket.Usage{Model: "gpt://private-folder/aliceai-llm-flash", InputTokens: 10, OutputTokens: 5}
	switch input.Title {
	case "failure":
		return ticket.Analysis{}, usage, &llm.Error{Code: "provider_http_429", Retryable: true}
	case "wrong category":
		return ticket.Analysis{Category: "other", DraftReply: "Уточните вопрос."}, usage, nil
	default:
		return ticket.Analysis{Category: "billing", DraftReply: "Уточните дату списания."}, usage, nil
	}
}

func TestEvaluationReportIncludesFailuresAndDrafts(t *testing.T) {
	t.Parallel()
	cases := []evaluationCase{
		{Input: ticket.Input{Title: "correct", Message: "Двойное списание"}, ExpectedCategory: "billing"},
		{Input: ticket.Input{Title: "wrong category", Message: "Нет доступа"}, ExpectedCategory: "access"},
		{Input: ticket.Input{Title: "failure", Message: "Ошибка сервера"}, ExpectedCategory: "technical"},
	}
	report := evaluate(t.Context(), evaluationFake{}, cases, time.Second, io.Discard)
	if report.Correct != 1 || report.Total != 3 || report.Failures != 1 || report.InputTokens != 30 || report.OutputTokens != 15 {
		t.Errorf("evaluate(three cases) counters = %+v, want correct=1 total=3 failures=1 input=30 output=15", report)
	}
	if len(report.Cases) != 3 {
		t.Fatalf("evaluate(three cases) returned %d cases, want 3", len(report.Cases))
	}
	if report.Cases[0].Analysis == nil || report.Cases[0].Analysis.DraftReply != "Уточните дату списания." {
		t.Errorf("evaluate(correct) analysis = %+v, want saved draft", report.Cases[0].Analysis)
	}
	if report.Cases[2].Analysis != nil || report.Cases[2].Error != "provider_http_429" {
		t.Errorf("evaluate(failure) = %+v, want error code and no analysis", report.Cases[2])
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "private-folder") || !strings.Contains(string(data), `"model":"aliceai-llm-flash"`) {
		t.Errorf("evaluate report JSON = %s, want short model name without folder ID", data)
	}
}
