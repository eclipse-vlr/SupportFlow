package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"supportflow/internal/ticket"
)

func TestNormalizeReplyPreservesMeaning(t *testing.T) {
	input := " \r\n  Здравствуйте! Уточните дату.\u00a0\r\n\r\n\r\nНомер заказа: АБ-105. Сумма: 1 250,50 ₽.\r\n "
	want := "Здравствуйте! Уточните дату.\n\nНомер заказа: АБ-105. Сумма: 1 250,50 ₽."
	if got := normalizeReply(input); got != want {
		t.Fatalf("normalizeReply = %q, want %q", got, want)
	}
	// A promise must not be silently removed, leaving a misleading fragment.
	promise := "Здравствуйте! Мы вернём деньги."
	if normalizeReply(promise) != promise {
		t.Fatal("normalization removed content")
	}
}

func TestReplyRejectsKnownArtifacts(t *testing.T) {
	for _, text := range []string{
		"Ответ требует редактирования.",
		"Черновик ответа:\nЗдравствуйте!",
		"Здравствуйте!\n\nДля оператора: проверить платёж.",
		"**Здравствуйте!** Уточните дату.",
		"__Здравствуйте!__ Уточните дату.",
		"Здравствуйте!\n- Уточните дату.",
		"# Ответ\nЗдравствуйте!",
		"```text\nЗдравствуйте!\n```",
		"<think>Нужно уточнить дату</think>Здравствуйте!",
		"<p>Здравствуйте!</p>",
		"Здравствуйте, [имя клиента]!",
		"Здравствуйте, {{customer_name}}!",
		`{"draft_reply":"Здравствуйте!"}`,
		"«Здравствуйте! Уточните дату.»",
		"Здравствуйте! Мы учтём предложение.",
		"Мы всё изучим и вернёмся с ответом.",
		"Проверим информацию и сообщим вам.",
		"Мы разберёмся в ситуации.",
		"Здравствуйте! Заявка передана специалистам.",
		"Согласно базе знаний уточните дату.",
		"См. billing-double-charge.",
		"Здравствуйте! citesource",
		"Здравствуйте! Уточните дату списания. [1]",
		"Здравствуйте!\nКатегория: billing",
		"Здрав\u200bствуйте! Уточните дату.",
		"Здравствуйте!\u202e Уточните дату.",
		"Здравствуйте! \ufffd Уточните дату.",
		"Для проверки уточните даты и суммы обеих операций.\n\nДля проверки уточните даты и суммы обеих операций.",
	} {
		t.Run(text, func(t *testing.T) {
			if issue := replyIssue(text, []ticket.Source{{ID: "billing-double-charge"}}); issue == "" {
				t.Fatalf("accepted defective reply: %q", text)
			}
		})
	}
}

func TestReplyPreservesOrdinaryCustomerText(t *testing.T) {
	for _, text := range []string{
		"Здравствуйте! Уточните дату и сумму списания. Не отправляйте пароли и коды подтверждения.",
		"Подтверждённых сведений о сроках появления этой функции нет.",
		"«E-500» — это текст ошибки? Уточните время её появления.",
		`Уточните, возникает ли ошибка для файла C:\new\report_2026.csv.`,
		`Уточните, возникает ли ошибка для файла C:\new\report  final.csv.`,
		"Появляется ошибка в __init__.py? Сам файл присылать не нужно.",
		"Сервер вернул <nil>? Уточните время ошибки.",
		"Спасибо, Анна-Мария! Номер заказа АБ-105, сумма 1 250,50 ₽.",
		"Спасибо за обратную связь! Рады, что вопрос удалось решить.",
	} {
		if normalized := normalizeReply(text); normalized != text {
			t.Errorf("normalization changed ordinary content: %q", normalized)
		}
		if issue := replyIssue(text, nil); issue != "" {
			t.Errorf("rejected ordinary reply %q: %s", text, issue)
		}
	}
}

func draftAnalysisJSON(reply string) string {
	analysis := ticket.Analysis{Category: "billing", Summary: "Двойное списание", Priority: "medium", PriorityReason: "Один пользователь", DraftReply: reply}
	data, _ := json.Marshal(analysis)
	return string(data)
}

func TestDraftRewriteIsBoundedAndAccountsForBothCalls(t *testing.T) {
	const clean = "Здравствуйте! Уточните даты и суммы обеих операций. Эти данные нужны для проверки платежей."
	for _, tc := range []struct {
		name, first, second, wantCode string
		calls                         int
	}{
		{"clean", "  " + clean + "\r\n", "", "", 1},
		{"service marker repaired", "Требует редактирования", clean, "", 2},
		{"promise repaired", "Мы всё проверим и сообщим вам.", clean, "", 2},
		{"persistent artifact", "Черновик ответа: **Здравствуйте!**", "Требует редактирования", "provider_reply_needs_editing", 2},
		{"schema error", "invalid-json", "", "provider_invalid_analysis", 1},
		{"failed rewrite", "Требует редактирования", "http-error", "provider_http_503", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			feedback := make(chan string, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				index := calls.Add(1)
				var payload struct {
					Input string `json:"input"`
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				var input struct {
					Feedback string `json:"reply_feedback"`
				}
				if err := json.Unmarshal([]byte(payload.Input), &input); err != nil {
					t.Error(err)
				}
				feedback <- input.Feedback
				reply := tc.first
				if index > 1 {
					reply = tc.second
				}
				if reply == "http-error" {
					w.WriteHeader(503)
					return
				}
				body := draftAnalysisJSON(reply)
				if reply == "invalid-json" {
					body = "{"
				}
				fmt.Fprint(w, envelope("completed", body))
			}))
			defer server.Close()
			client := New(server.URL, "test-secret", "test-model", 1000, time.Second)
			defer client.Close()
			analysis, usage, err := client.Analyze(t.Context(), ticket.Input{Title: "Оплата", Message: "Дважды списали деньги"})
			if int(calls.Load()) != tc.calls {
				t.Fatalf("calls=%d, want %d", calls.Load(), tc.calls)
			}
			if first := <-feedback; first != "" {
				t.Error("first call unexpectedly had feedback")
			}
			if tc.calls == 2 && <-feedback == "" {
				t.Error("rewrite did not explain the defect")
			}
			if tc.wantCode != "" {
				var providerErr *Error
				if !errors.As(err, &providerErr) || providerErr.Code != tc.wantCode || analysis.DraftReply != "" {
					t.Fatalf("analysis=%+v err=%v, want %s and no draft", analysis, err, tc.wantCode)
				}
				if tc.wantCode == "provider_reply_needs_editing" && providerErr.Retryable {
					t.Error("quality failure scheduled another retry")
				}
			} else if err != nil || analysis.DraftReply != clean || len(analysis.Sources) == 0 {
				t.Fatalf("analysis=%+v err=%v, want clean draft and sources", analysis, err)
			}
			paidResponses := tc.calls
			if tc.second == "http-error" {
				paidResponses--
			}
			if usage.InputTokens != int64(12*paidResponses) || usage.OutputTokens != int64(9*paidResponses) {
				t.Fatalf("usage=%+v, want usage for %d responses", usage, paidResponses)
			}
		})
	}
}

func TestCancellationStopsRewriteWithoutLosingUsage(t *testing.T) {
	var calls atomic.Int32
	started, finished := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if calls.Add(1) == 1 {
			fmt.Fprint(w, envelope("completed", draftAnalysisJSON("Требует редактирования")))
			return
		}
		close(started)
		<-r.Context().Done()
		close(finished)
	}))
	defer server.Close()
	client := New(server.URL, "test-secret", "test-model", 1000, time.Second)
	defer client.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	type result struct {
		usage ticket.Usage
		err   error
	}
	done := make(chan result, 1)
	go func() {
		_, usage, err := client.Analyze(ctx, ticket.Input{})
		done <- result{usage, err}
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("rewrite did not start")
	}
	cancel()
	select {
	case got := <-done:
		var providerErr *Error
		if !errors.As(got.err, &providerErr) || providerErr.Code != "provider_timeout" || got.usage.InputTokens != 12 || got.usage.OutputTokens != 9 {
			t.Fatalf("canceled rewrite: %+v, want first call usage and timeout", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("rewrite ignored caller cancellation")
	}
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream rewrite remained active")
	}
	if calls.Load() != 2 {
		t.Fatalf("calls=%d, want 2", calls.Load())
	}
}

func TestRewriteRejectsUsageOverflow(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		input := int64(1)
		reply := "Здравствуйте! Уточните дату оплаты."
		if calls.Add(1) == 1 {
			input = math.MaxInt64
			reply = "Требует редактирования"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "completed", "model": "test-model", "usage": map[string]int64{"input_tokens": input, "output_tokens": 9},
			"output": []any{map[string]any{"type": "message", "content": []any{map[string]string{"type": "output_text", "text": draftAnalysisJSON(reply)}}}},
		})
	}))
	defer server.Close()
	client := New(server.URL, "test-secret", "test-model", 1000, time.Second)
	defer client.Close()
	_, usage, err := client.Analyze(t.Context(), ticket.Input{})
	var providerErr *Error
	if !errors.As(err, &providerErr) || providerErr.Code != "provider_invalid_usage" || usage.InputTokens != math.MaxInt64 || usage.OutputTokens != 9 {
		t.Fatalf("usage=%+v err=%v, want overflow rejected without wrapping counters", usage, err)
	}
}
