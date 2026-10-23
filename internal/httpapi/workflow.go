package httpapi

import (
	"net/http"
	"strings"

	"supportflow/internal/ticket"
)

func (a *API) closure(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !idPattern.MatchString(id) {
		problem(w, 400, "invalid_ticket_id")
		return
	}
	var input struct {
		Closed          *bool  `json:"closed"`
		ExpectedVersion *int64 `json:"expected_version"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.Closed == nil {
		problem(w, 400, "invalid_closed")
		return
	}
	if input.ExpectedVersion == nil || *input.ExpectedVersion < 0 {
		problem(w, 400, "invalid_expected_version")
		return
	}
	item, err := a.store.SetClosed(r.Context(), id, *input.Closed, *input.ExpectedVersion)
	if err != nil {
		a.failure(w, err)
		return
	}
	write(w, 200, item)
}

func (a *API) feedback(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !idPattern.MatchString(id) {
		problem(w, 400, "invalid_ticket_id")
		return
	}
	var input struct {
		ticket.Feedback
		ExpectedVersion *int64 `json:"expected_version"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.ExpectedVersion == nil || *input.ExpectedVersion < 0 {
		problem(w, 400, "invalid_expected_version")
		return
	}
	input.Reason = strings.TrimSpace(input.Reason)
	if err := input.Feedback.Validate(); err != nil {
		problem(w, 400, "invalid_feedback")
		return
	}
	item, err := a.store.SetFeedback(r.Context(), id, input.Feedback, *input.ExpectedVersion)
	if err != nil {
		a.failure(w, err)
		return
	}
	write(w, 200, item)
}
