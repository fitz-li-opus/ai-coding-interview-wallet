package ingest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"wallet/internal/db"
	"wallet/internal/webhookauth"
)

// The one unrecoverable failure in this system is answering 2xx to an event
// that was not stored: the provider throws it away and the credits are gone.
// So "the database is not there" must come out as a non-2xx, and the pool must
// recover on its own once it is back — a service that needs restarting after a
// database blip turns a blip into an outage.
//
//	TEST_DATABASE_URL=... TEST_PG_CONTAINER=<name> go test ./internal/ingest/
func TestStorageDownIsRefusedAndRecoversByItself(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	container := os.Getenv("TEST_PG_CONTAINER")
	if dsn == "" || container == "" {
		t.Skip("set TEST_DATABASE_URL and TEST_PG_CONTAINER to run the storage-outage test")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is needed to stop and start the database")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pool, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE wallets, wallet_entries, webhook_events, idempotency_keys`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	const secret = "whsec_interview_test"
	handler := NewHandler(pool, secret, webhookauth.DefaultTolerance)

	body := event(func(e *map[string]any) {
		(*e)["id"] = "evt_storage_down"
		payment(e)["id"] = "pay_storage_down"
		payment(e)["user_id"] = "u_down"
	})

	deliver := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/webhooks/payments", strings.NewReader(body))
		req.Header.Set(webhookauth.HeaderName, webhookauth.Sign([]byte(body), secret, time.Now()))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req.WithContext(ctx))
		return rec
	}

	docker := func(args ...string) {
		t.Helper()
		if out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
			t.Fatalf("docker %v: %v: %s", args, err, out)
		}
	}

	docker("stop", container)
	restarted := false
	defer func() {
		if !restarted {
			docker("start", container)
		}
	}()

	rec := deliver()
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("database down: want 503, got %d (%s)", rec.Code, rec.Body.String())
	}
	var refusal map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &refusal); err != nil {
		t.Fatalf("body is not JSON: %s", rec.Body.String())
	}
	if refusal["error"] != "STORAGE_UNAVAILABLE" {
		t.Fatalf("want STORAGE_UNAVAILABLE, got %q", refusal["error"])
	}

	docker("start", container)
	restarted = true

	// No restart of the service, no new pool: the same handler must work again
	// once the database answers, because the provider is about to redeliver.
	var ok bool
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); {
		if rec := deliver(); rec.Code == http.StatusOK {
			ok = true
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !ok {
		t.Fatal("the handler never recovered after the database came back")
	}

	var entries, balance int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM wallet_entries WHERE payment_id = 'pay_storage_down'`).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	if entries != 1 {
		t.Fatalf("ledger rows = %d, want exactly 1 — the refused delivery must have written nothing", entries)
	}
	if err := pool.QueryRow(ctx, `SELECT balance FROM wallets WHERE user_id = 'u_down'`).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if balance != 500 {
		t.Fatalf("balance = %d, want 500", balance)
	}
}
