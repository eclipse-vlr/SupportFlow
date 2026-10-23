// Package diagnostics describes failures without logging credentials or user text.
package diagnostics

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"syscall"

	"github.com/jackc/pgx/v5/pgconn"
)

type operationError struct {
	operation string
	cause     error
}

func (e *operationError) Error() string { return e.operation }
func (e *operationError) Unwrap() error { return e.cause }

// Wrap preserves error identity while exposing only a caller-controlled operation.
// The operation must not contain credentials, request bodies or upstream messages.
func Wrap(operation string, err error) error {
	return &operationError{operation: operation, cause: err}
}

// Error returns allowlisted diagnostics, never the original error message.
func Error(err error) slog.Value {
	fields := []slog.Attr{slog.String("type", fmt.Sprintf("%T", err))}
	var operation *operationError
	if errors.As(err, &operation) {
		fields = append(fields, slog.String("operation", operation.operation))
	}
	kind := "internal"
	var database *pgconn.PgError
	var network net.Error
	switch {
	case errors.Is(err, context.Canceled):
		kind = "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		kind = "timeout"
	case errors.As(err, &database):
		kind = "postgres"
		fields = append(fields, slog.String("sqlstate", database.SQLState()))
	case errors.As(err, &network):
		kind = "network"
		fields = append(fields, slog.Bool("timeout", network.Timeout()))
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		fields = append(fields, slog.Uint64("errno", uint64(errno)))
	}
	return slog.GroupValue(append(fields, slog.String("kind", kind))...)
}
