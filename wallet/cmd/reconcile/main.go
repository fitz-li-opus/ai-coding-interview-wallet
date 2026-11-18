// Command reconcile makes one reconciliation pass and exits.
//
// It is a one-shot maintenance command rather than a scheduled worker, and it
// does not need the wallet service to be running: it opens the database itself,
// reads the provider's event list, and applies what is missing. The BDD step
// `the reconciler runs` executes this binary inside the wallet container, which
// is why it ships in the same image.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"wallet/internal/db"
	"wallet/internal/reconcile"
)

// timeout bounds the whole pass. A reconciler that hangs holds nothing open but
// tells nobody anything, which is worse than one that fails and says so.
const timeout = 60 * time.Second

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "reconcile: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}
	providerBaseURL := os.Getenv("PROVIDER_BASE_URL")
	if providerBaseURL == "" {
		return fmt.Errorf("PROVIDER_BASE_URL is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// No migration here: the schema belongs to the service, and a maintenance
	// command that creates tables is a maintenance command that can mask a
	// misconfigured database.
	pool, err := db.Open(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()

	result, err := reconcile.Run(ctx, pool, providerBaseURL)
	if err != nil {
		// The result so far is still printed on stderr-adjacent failure paths
		// by the caller's logs; the error is what decides the exit code.
		return err
	}

	// One line of JSON on stdout, so a step or a human can read the same thing.
	line, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode result: %w", err)
	}
	fmt.Println(string(line))
	return nil
}
