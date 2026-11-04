package diagnostics

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"syscall"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestDiagnosticsPreserveCauseWithoutSensitiveMessages(t *testing.T) {
	t.Parallel()
	const secret = "password-and-customer-message"
	for _, tc := range []struct {
		name, kind string
		err        error
	}{
		{"postgres", "postgres", &pgconn.PgError{Code: "42501", Message: secret, Detail: secret}},
		{"network", "network", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}},
		{"deadline", "timeout", fmt.Errorf("%s: %w", secret, context.DeadlineExceeded)},
		{"cancellation", "canceled", context.Canceled},
		{"opaque", "internal", errors.New(secret)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			wrapped := Wrap("database migration", tc.err)
			if !errors.Is(wrapped, tc.err) || strings.Contains(wrapped.Error(), secret) {
				t.Fatalf("Wrap() = %v, want safe operation preserving cause", wrapped)
			}
			var output bytes.Buffer
			slog.New(slog.NewJSONHandler(&output, nil)).Error("operation failed", "err", Error(wrapped))
			if strings.Contains(output.String(), secret) {
				t.Fatal("diagnostics exposed a sensitive error message")
			}
			var record struct {
				Err struct {
					Kind, Operation, SQLState string
					Errno                     uint64
				} `json:"err"`
			}
			if err := json.Unmarshal(output.Bytes(), &record); err != nil {
				t.Fatal(err)
			}
			if record.Err.Kind != tc.kind || record.Err.Operation != "database migration" {
				t.Errorf("diagnostics = %+v, want kind=%s and migration operation", record.Err, tc.kind)
			}
			if tc.name == "postgres" && record.Err.SQLState != "42501" {
				t.Errorf("SQLState = %q, want 42501", record.Err.SQLState)
			}
			if tc.name == "network" && record.Err.Errno != uint64(syscall.ECONNREFUSED) {
				t.Errorf("errno = %d, want ECONNREFUSED", record.Err.Errno)
			}
		})
	}
}
