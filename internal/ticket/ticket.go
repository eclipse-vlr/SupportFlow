// Package ticket defines the support workflow independently of HTTP, SQL and LLM providers.
package ticket

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrNotFound  = errors.New("ticket not found")
	ErrConflict  = errors.New("ticket state or idempotency conflict")
	ErrQueueFull = errors.New("queue is full")
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
	Category       string `json:"category"`
	Summary        string `json:"summary"`
	Priority       string `json:"priority"`
	PriorityReason string `json:"priority_reason"`
	DraftReply     string `json:"draft_reply"`
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

func (r Review) Validate() error {
	if !ValidCategory(r.Category) || strings.TrimSpace(r.DraftReply) == "" || len(r.DraftReply) > 12000 || !utf8.ValidString(r.DraftReply) || strings.ContainsRune(r.DraftReply, '\x00') {
		return errors.New("invalid review category or reply")
	}
	return nil
}

type Ticket struct {
	ID string `json:"id"`
	Input
	Status    string    `json:"status"`
	Attempts  int       `json:"attempts"`
	Analysis  *Analysis `json:"analysis"`
	Review    *Review   `json:"review"`
	Usage     Usage     `json:"usage"`
	LastError string    `json:"last_error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
