package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type startupHandler struct {
	slog.Handler
	started chan string
}

func (h startupHandler) Handle(ctx context.Context, record slog.Record) error {
	if record.Message == "supportflow started" {
		record.Attrs(func(attr slog.Attr) bool {
			if attr.Key == "address" {
				h.started <- attr.Value.String()
			}
			return true
		})
	}
	return h.Handler.Handle(ctx, record)
}

func serviceDatabase(t *testing.T) (*pgx.Conn, string) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for service integration tests")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := pgx.Identifier{"service_test_" + rand.Text()}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		defer admin.Close(cleanup)
		if _, err := admin.Exec(cleanup, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	t.Setenv("DATABASE_URL", parsed.String())
	t.Setenv("HTTP_ADDR", "127.0.0.1:0")
	t.Setenv("LLM_PROVIDER", "demo")
	return admin, schema
}

func TestServiceStartsAndStopsWithItsContext(t *testing.T) {
	serviceDatabase(t)
	ctx, cancel := context.WithCancel(t.Context())
	var output bytes.Buffer
	started := make(chan string, 1)
	log := slog.New(startupHandler{Handler: slog.NewJSONHandler(&output, nil), started: started})
	done := make(chan struct{})
	var runErr error
	go func() {
		defer close(done)
		runErr = run(ctx, log)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("service did not stop after cancellation")
		}
	})
	var address string
	select {
	case address = <-started:
	case <-done:
		t.Fatalf("run() stopped before serving: %v", runErr)
	case <-time.After(10 * time.Second):
		t.Fatal("service did not start")
	}
	client := &http.Client{Timeout: time.Second}
	for _, path := range []string{"/healthz", "/readyz", "/api/v1/tickets"} {
		response, err := client.Get("http://" + address + path)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Errorf("GET %s status = %d, want 200", path, response.StatusCode)
		}
	}
	client.CloseIdleConnections()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("service did not join HTTP and worker goroutines")
	}
	if runErr != nil || !strings.Contains(output.String(), "supportflow stopped") {
		t.Errorf("run(canceled context) error = %v log = %s, want graceful shutdown", runErr, &output)
	}
	connection, err := net.DialTimeout("tcp", address, time.Second)
	if err == nil {
		connection.Close()
		t.Error("HTTP listener is still accepting connections after shutdown")
	}
}

func TestServiceRejectsNewerSchemaBeforeListening(t *testing.T) {
	admin, schema := serviceDatabase(t)
	if _, err := admin.Exec(t.Context(), "CREATE TABLE "+schema+".schema_migrations (version integer PRIMARY KEY); INSERT INTO "+schema+".schema_migrations VALUES (99)"); err != nil {
		t.Fatal(err)
	}
	err := run(t.Context(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil || err.Error() != "database migration" {
		t.Errorf("run(newer schema) error = %v, want migration startup failure", err)
	}
}

func TestStartupDoesNotExposeMalformedDatabaseURL(t *testing.T) {
	const secret = "private-password"
	t.Setenv("LLM_PROVIDER", "demo")
	t.Setenv("DATABASE_URL", "postgres://user:"+secret+"@localhost:invalid/db")
	err := run(t.Context(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil || err.Error() != "parse DATABASE_URL" || strings.Contains(err.Error(), secret) {
		t.Errorf("run(malformed DATABASE_URL) error = %v, want safe configuration failure", err)
	}
}
