package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"wallet/internal/webhookauth"
)

// This is the only test that proves the signature check agrees with the thing
// it has to agree with. The hard-coded vector in webhookauth_test.go proves the
// algorithm; this proves the wire — header name and format, the exact bytes the
// provider's JSON encoder emits, the timestamp it stamps per attempt.
//
// It runs services/payment-provider unmodified (that directory is read-only to
// us) and configures it only through its environment, which is the same thing
// the BDD stack does with containers.
func TestTheRealProviderCreditsTheWallet(t *testing.T) {
	pool := testPool(t) // skips unless TEST_DATABASE_URL is set
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is needed to run the real provider")
	}

	const secret = "whsec_interview_test"
	var delivered struct {
		count int
		last  string
	}

	wallet := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		delivered.count++
		delivered.last = r.Header.Get(webhookauth.HeaderName)
		NewHandler(pool, secret, webhookauth.DefaultTolerance).ServeHTTP(w, r)
	}))
	defer wallet.Close()

	port := freePort(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	provider := exec.CommandContext(ctx, "python3", "../../../services/payment-provider/provider.py")
	provider.Env = append(os.Environ(),
		"PORT="+port,
		"WEBHOOK_URL="+wallet.URL+"/webhooks/payments",
		"WEBHOOK_SECRET="+secret,
		"DUPLICATE_RATE=0",
	)
	if err := provider.Start(); err != nil {
		t.Fatalf("start provider: %v", err)
	}
	defer func() { _ = provider.Process.Kill() }()

	base := "http://127.0.0.1:" + port
	waitFor(t, base+"/health")

	// Create a payment the way a buyer would, and let the provider sign and
	// deliver on its own schedule.
	paymentID := createPayment(t, base, `{"user_id":"u_live","amount_cents":750}`)

	deadline := time.Now().Add(30 * time.Second)
	for {
		var balance int64
		err := pool.QueryRow(ctx, `SELECT balance FROM wallets WHERE user_id = 'u_live'`).Scan(&balance)
		if err == nil && balance == 750 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the provider's own webhook never credited the wallet (deliveries=%d, last header=%q, err=%v)",
				delivered.count, delivered.last, err)
		}
		time.Sleep(100 * time.Millisecond)
	}

	var n int64
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM wallet_entries
		 WHERE user_id = 'u_live' AND kind = 'credit' AND amount = 750
		   AND payment_id = $1 AND event_id IS NOT NULL`, paymentID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("ledger rows for the live payment = %d, want 1", n)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM webhook_events
		 WHERE payment_id = $1 AND type = 'payment.succeeded'
		   AND status = 'succeeded' AND outcome = 'applied'`, paymentID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("inbox rows for the live payment = %d, want 1", n)
	}

	// And the refund, which is the same payment id under a different event id.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/payments/"+paymentID+"/refund", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("refund: %v", err)
	}
	_ = resp.Body.Close()

	deadline = time.Now().Add(30 * time.Second)
	for {
		var balance int64
		err := pool.QueryRow(ctx, `SELECT balance FROM wallets WHERE user_id = 'u_live'`).Scan(&balance)
		if err == nil && balance == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the refund never reversed the credit (deliveries=%d, err=%v)", delivered.count, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM webhook_events WHERE payment_id = $1`, paymentID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("inbox rows after the refund = %d, want 2 — the key is the event, not the payment", n)
	}
}

func createPayment(t *testing.T, base, body string) string {
	t.Helper()
	resp, err := http.Post(base+"/payments", "application/json", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("create payment: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var payment struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payment); err != nil {
		t.Fatalf("decode payment: %v", err)
	}
	if payment.ID == "" {
		t.Fatal("provider returned no payment id")
	}
	return payment.ID
}

func waitFor(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s never became healthy", url)
}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := (&http.Server{}).ListenAndServe, error(nil)
	_ = l
	_ = err
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := srv.Listener.Addr().(interface{ String() string }).String()
	srv.Close()
	_, port, ok := cutLast(addr, ":")
	if !ok {
		t.Fatalf("cannot find a port in %q", addr)
	}
	if _, err := strconv.Atoi(port); err != nil {
		t.Fatalf("port %q is not a number", port)
	}
	return port
}

func cutLast(s, sep string) (before, after string, found bool) {
	for i := len(s) - len(sep); i >= 0; i-- {
		if s[i:i+len(sep)] == sep {
			return s[:i], s[i+len(sep):], true
		}
	}
	return s, "", false
}
