package httpapi

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"supportflow/internal/ticket"
)

//go:embed web/*
var web embed.FS

type Repository interface {
	Create(context.Context, ticket.Input, string) (ticket.Ticket, bool, error)
	Get(context.Context, string) (ticket.Ticket, error)
	List(context.Context, string, string, int, int) ([]ticket.Ticket, error)
	Review(context.Context, string, ticket.Review, int64) (ticket.Ticket, error)
	History(context.Context, string, int64, int) ([]ticket.ReviewEvent, error)
	Ping(context.Context) error
}

type API struct {
	store    Repository
	log      *slog.Logger
	provider string
}

var idPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var keyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

func New(store Repository, log *slog.Logger, provider string) http.Handler {
	a := &API{store: store, log: log, provider: provider}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/tickets", a.create)
	mux.HandleFunc("GET /api/v1/tickets", a.list)
	mux.HandleFunc("GET /api/v1/tickets/{id}", a.get)
	mux.HandleFunc("PATCH /api/v1/tickets/{id}/review", a.review)
	mux.HandleFunc("GET /api/v1/tickets/{id}/history", a.history)
	mux.HandleFunc("GET /api/v1/tickets/{id}/sources/{sourceID}", a.source)
	mux.HandleFunc("GET /api/v1/config", func(w http.ResponseWriter, r *http.Request) { write(w, 200, map[string]string{"provider": provider}) })
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { write(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if err := store.Ping(ctx); err != nil {
			problem(w, 503, "database_unavailable")
			return
		}
		write(w, 200, map[string]string{"status": "ready"})
	})
	assets, _ := fs.Sub(web, "web")
	mux.Handle("GET /", http.FileServer(http.FS(assets)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		// Reject cross-site browser submissions; this local MVP has no user accounts.
		if r.Method != "GET" && r.Method != "HEAD" && r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			problem(w, 403, "cross_site_request")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		mux.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (a *API) create(w http.ResponseWriter, r *http.Request) {
	var input ticket.Input
	if !decode(w, r, &input) {
		return
	}
	input.Title = strings.TrimSpace(input.Title)
	input.Message = strings.TrimSpace(input.Message)
	if err := input.Validate(); err != nil {
		problem(w, 400, err.Error())
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		key = rand.Text()
	} else if !keyPattern.MatchString(key) {
		problem(w, 400, "invalid_idempotency_key")
		return
	}
	t, created, err := a.store.Create(r.Context(), input, key)
	if err != nil {
		a.failure(w, err)
		return
	}
	w.Header().Set("Location", "/api/v1/tickets/"+t.ID)
	status := http.StatusOK
	if created {
		status = http.StatusAccepted
	}
	write(w, status, t)
}

func (a *API) get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !idPattern.MatchString(id) {
		problem(w, 400, "invalid_ticket_id")
		return
	}
	t, err := a.store.Get(r.Context(), id)
	if err != nil {
		a.failure(w, err)
		return
	}
	write(w, 200, t)
}

func (a *API) list(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	category, status := query.Get("category"), query.Get("status")
	if category != "" && !ticket.ValidCategory(category) {
		problem(w, 400, "invalid_category")
		return
	}
	if status != "" && status != "pending" && status != "processing" && status != "completed" && status != "failed" {
		problem(w, 400, "invalid_status")
		return
	}
	limit, offset := 50, 0
	var err error
	if query.Has("limit") {
		limit, err = strconv.Atoi(query.Get("limit"))
		if err != nil || limit < 1 || limit > 100 {
			problem(w, 400, "invalid_limit")
			return
		}
	}
	if query.Has("offset") {
		offset, err = strconv.Atoi(query.Get("offset"))
		if err != nil || offset < 0 || offset > 10000 {
			problem(w, 400, "invalid_offset")
			return
		}
	}
	items, err := a.store.List(r.Context(), category, status, limit, offset)
	if err != nil {
		a.failure(w, err)
		return
	}
	write(w, 200, map[string]any{"items": items, "limit": limit, "offset": offset})
}

func (a *API) source(w http.ResponseWriter, r *http.Request) {
	id, sourceID := r.PathValue("id"), r.PathValue("sourceID")
	if !idPattern.MatchString(id) {
		problem(w, 400, "invalid_ticket_id")
		return
	}
	if !ticket.ValidSourceID(sourceID) {
		problem(w, 400, "invalid_source_id")
		return
	}
	item, err := a.store.Get(r.Context(), id)
	if err != nil {
		a.failure(w, err)
		return
	}
	if item.Analysis != nil {
		for _, source := range item.Analysis.Sources {
			if source.ID == sourceID {
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				_, _ = fmt.Fprintf(w, "%s\n\n%s\n\nВерсия: %s\n", source.Title, source.Content, source.Version)
				return
			}
		}
	}
	problem(w, 404, "source_not_found")
}

func (a *API) review(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !idPattern.MatchString(id) {
		problem(w, 400, "invalid_ticket_id")
		return
	}
	var input struct {
		ticket.Review
		ExpectedRevision *int64 `json:"expected_revision"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.ExpectedRevision == nil || *input.ExpectedRevision < 0 {
		problem(w, 400, "invalid_expected_revision")
		return
	}
	if err := input.Review.Validate(); err != nil {
		problem(w, 400, err.Error())
		return
	}
	t, err := a.store.Review(r.Context(), id, input.Review, *input.ExpectedRevision)
	if err != nil {
		a.failure(w, err)
		return
	}
	write(w, 200, t)
}

func (a *API) history(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !idPattern.MatchString(id) {
		problem(w, 400, "invalid_ticket_id")
		return
	}
	query := r.URL.Query()
	limit, before := 10, int64(0)
	var err error
	if query.Has("limit") {
		limit, err = strconv.Atoi(query.Get("limit"))
		if err != nil || limit < 1 || limit > 50 {
			problem(w, 400, "invalid_limit")
			return
		}
	}
	if query.Has("before_revision") {
		before, err = strconv.ParseInt(query.Get("before_revision"), 10, 64)
		if err != nil || before < 1 {
			problem(w, 400, "invalid_history_cursor")
			return
		}
	}
	items, err := a.store.History(r.Context(), id, before, limit)
	if err != nil {
		a.failure(w, err)
		return
	}
	var next int64
	if len(items) == limit {
		next = items[len(items)-1].Revision
	}
	write(w, 200, map[string]any{"items": items, "next_before_revision": next})
}

func decode(w http.ResponseWriter, r *http.Request, dest any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" || (r.Header.Get("Content-Encoding") != "" && r.Header.Get("Content-Encoding") != "identity") {
		problem(w, 415, "expected_application_json")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32*1024)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			problem(w, 413, "request_too_large")
		} else {
			problem(w, 400, "invalid_json")
		}
		return false
	}
	if !utf8.Valid(data) {
		problem(w, 400, "invalid_utf8")
		return false
	}
	// All request DTOs are flat objects: reject duplicate names before decoding.
	keys := map[string]bool{}
	tokens := json.NewDecoder(strings.NewReader(string(data)))
	first, err := tokens.Token()
	if err != nil || first != json.Delim('{') {
		problem(w, 400, "expected_json_object")
		return false
	}
	for tokens.More() {
		name, err := tokens.Token()
		key, ok := name.(string)
		if err != nil || !ok || keys[strings.ToLower(key)] {
			problem(w, 400, "duplicate_or_invalid_field")
			return false
		}
		keys[strings.ToLower(key)] = true
		var raw json.RawMessage
		if err = tokens.Decode(&raw); err != nil {
			problem(w, 400, "invalid_json")
			return false
		}
	}
	if _, err = tokens.Token(); err != nil {
		problem(w, 400, "invalid_json")
		return false
	}
	if err = tokens.Decode(new(any)); !errors.Is(err, io.EOF) {
		problem(w, 400, "expected_single_json_object")
		return false
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(dest); err != nil {
		problem(w, 400, "invalid_json_fields")
		return false
	}
	return true
}

func (a *API) failure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ticket.ErrNotFound):
		problem(w, 404, "ticket_not_found")
	case errors.Is(err, ticket.ErrConflict):
		problem(w, 409, "ticket_conflict")
	case errors.Is(err, ticket.ErrReviewConflict):
		problem(w, 409, "review_conflict")
	case errors.Is(err, ticket.ErrQueueFull):
		w.Header().Set("Retry-After", "10")
		problem(w, 429, "queue_full")
	default:
		a.log.Error("repository operation failed")
		problem(w, 500, "internal_error")
	}
}

func write(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func problem(w http.ResponseWriter, status int, code string) {
	write(w, status, map[string]string{"error": code})
}
