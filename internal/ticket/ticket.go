// Package ticket defines the support workflow independently of HTTP, SQL and LLM providers.
package ticket

import (
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrNotFound       = errors.New("ticket not found")
	ErrConflict       = errors.New("ticket state or idempotency conflict")
	ErrQueueFull      = errors.New("queue is full")
	ErrReviewConflict = errors.New("review revision changed")
)

const MaxMessageBytes = 16 * 1024

type Input struct {
	Title   string `json:"title"`
	Message string `json:"message"`
}

func (i Input) Validate() error {
	if strings.TrimSpace(i.Title) == "" || utf8.RuneCountInString(i.Title) > 200 {
		return errors.New("title must contain 1 to 200 characters")
	}
	if strings.TrimSpace(i.Message) == "" || len(i.Message) > MaxMessageBytes {
		return errors.New("message must contain 1 to 16384 bytes")
	}
	if !utf8.ValidString(i.Title) || !utf8.ValidString(i.Message) || strings.ContainsRune(i.Title+i.Message, '\x00') {
		return errors.New("text must be valid UTF-8 without NUL")
	}
	return nil
}

type Analysis struct {
	Category       string   `json:"category"`
	Summary        string   `json:"summary"`
	Priority       string   `json:"priority"`
	PriorityReason string   `json:"priority_reason"`
	DraftReply     string   `json:"draft_reply"`
	Sources        []Source `json:"sources,omitempty"`
}

// Source is the article snapshot included in the model request, not a model citation.
type Source struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Content string `json:"content"`
	Version string `json:"version"`
}

var sourceIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,79}$`)
var sourceVersionPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func ValidSourceID(id string) bool { return sourceIDPattern.MatchString(id) }

func (s Source) Validate() error {
	if !ValidSourceID(s.ID) || !sourceVersionPattern.MatchString(s.Version) ||
		strings.TrimSpace(s.Title) == "" || utf8.RuneCountInString(s.Title) > 200 ||
		strings.TrimSpace(s.Content) == "" || len(s.Content) > 4096 ||
		!utf8.ValidString(s.Title+s.Content) || strings.ContainsRune(s.Title+s.Content, '\x00') {
		return errors.New("invalid source snapshot")
	}
	return nil
}

func ValidCategory(s string) bool {
	return s == "billing" || s == "access" || s == "technical" || s == "other"
}

func (a Analysis) Validate() error {
	if !ValidCategory(a.Category) || (a.Priority != "low" && a.Priority != "medium" && a.Priority != "high") {
		return errors.New("invalid category or priority")
	}
	for _, value := range []string{a.Summary, a.PriorityReason, a.DraftReply} {
		if strings.TrimSpace(value) == "" || len(value) > 12000 || !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
			return errors.New("invalid analysis text")
		}
	}
	if len(a.Sources) > 3 {
		return errors.New("too many sources")
	}
	seen := make(map[string]bool, len(a.Sources))
	for _, source := range a.Sources {
		if err := source.Validate(); err != nil {
			return err
		}
		if seen[source.ID] {
			return errors.New("duplicate source")
		}
		seen[source.ID] = true
	}
	return nil
}

type Usage struct {
	Model        string `json:"model"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
}

type Review struct {
	Category   string `json:"category"`
	DraftReply string `json:"draft_reply"`
	Approved   bool   `json:"approved"`
}

// ReviewEvent is a saved snapshot; later edits never replace this version.
type ReviewEvent struct {
	Revision  int64     `json:"revision"`
	Kind      string    `json:"kind"`
	Review    Review    `json:"review"`
	CreatedAt time.Time `json:"created_at"`
}

func (r Review) Validate() error {
	if !ValidCategory(r.Category) || strings.TrimSpace(r.DraftReply) == "" || len(r.DraftReply) > 12000 || !utf8.ValidString(r.DraftReply) || strings.ContainsRune(r.DraftReply, '\x00') {
		return errors.New("invalid review category or reply")
	}
	return nil
}

type Ticket struct {
	ID string `json:"id"`
	Input
	Status         string    `json:"status"`
	Attempts       int       `json:"attempts"`
	Analysis       *Analysis `json:"analysis"`
	Review         *Review   `json:"review"`
	ReviewRevision int64     `json:"review_revision"`
	Usage          Usage     `json:"usage"`
	LastError      string    `json:"last_error,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}
