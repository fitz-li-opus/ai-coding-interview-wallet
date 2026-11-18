package bdd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cucumber/godog"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// tables are every table a scenario may touch, in an order that is safe to
// truncate as a set.
var tables = []string{"wallet_entries", "webhook_events", "idempotency_keys", "wallets"}

// openPool connects the harness to a scenario's own postgres.
//
// The simple query protocol is deliberate: a `in PostgreSQL:` docstring is a
// script — several statements separated by semicolons, which is how a fixture
// reads — and the extended protocol refuses more than one statement per call.
// The alternative would be splitting SQL on semicolons in the harness, which is
// a SQL parser nobody asked for.
func openPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	cfg.MaxConns = 8

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open pool: %w", err)
	}

	deadline := time.Now().Add(30 * time.Second)
	for {
		if err = pool.Ping(ctx); err == nil {
			return pool, nil
		}
		if time.Now().After(deadline) {
			pool.Close()
			return nil, fmt.Errorf("postgres never became reachable at %s: %w", dsn, err)
		}
		select {
		case <-ctx.Done():
			pool.Close()
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// schemaSQL reads the one definition of the schema: the same file the service
// embeds, so a harness that sets up a different shape than the service runs on
// is not possible.
func schemaSQL() (string, error) {
	root, err := walletModuleDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(root, "internal", "db", "migrations", "0001_init.sql")
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read schema %s: %w", path, err)
	}
	return string(b), nil
}

// RegisterInfraSteps registers its share of the registry. See steps.go.
func RegisterInfraSteps(sc *godog.ScenarioContext, w *World) {
	sc.Step(`^run migration$`, func(ctx context.Context) error {
		sql, err := schemaSQL()
		if err != nil {
			return err
		}
		// The service has already applied this schema on start-up; every
		// statement is IF NOT EXISTS, so applying it again is how the harness
		// states its precondition instead of assuming it.
		if _, err := w.PG.Exec(ctx, sql); err != nil {
			return fmt.Errorf("apply schema: %w", err)
		}
		// A scenario begins with nothing of anyone else's. Identity is reset so
		// that a ledger id in one scenario means the same thing as in another.
		if _, err := w.PG.Exec(ctx, fmt.Sprintf(
			"TRUNCATE %s RESTART IDENTITY", strings.Join(tables, ", "))); err != nil {
			return fmt.Errorf("truncate tables: %w", err)
		}
		return nil
	})

	sc.Step(`^in PostgreSQL:$`, func(ctx context.Context, doc *godog.DocString) error {
		sql, err := w.Subst(doc.Content)
		if err != nil {
			return err
		}
		if _, err := w.PG.Exec(ctx, sql); err != nil {
			return fmt.Errorf("exec fixture SQL: %w\n--- sql ---\n%s", err, sql)
		}
		return nil
	})

	sc.Step(`^in PostgreSQL query returns ([0-9]+) rows?:$`, func(ctx context.Context, want int, doc *godog.DocString) error {
		sql, err := w.Subst(doc.Content)
		if err != nil {
			return err
		}
		rows, err := w.PG.Query(ctx, sql)
		if err != nil {
			return fmt.Errorf("run query: %w\n--- sql ---\n%s", err, sql)
		}
		defer rows.Close()

		var got []string
		for rows.Next() {
			values, err := rows.Values()
			if err != nil {
				return fmt.Errorf("read row: %w", err)
			}
			got = append(got, fmt.Sprint(values))
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate rows: %w\n--- sql ---\n%s", err, sql)
		}
		if len(got) != want {
			// The rows themselves, because "expected 1 got 0" sends the reader
			// back to the database to ask the question the step just asked.
			return fmt.Errorf("expected %d row(s), got %d\n--- sql ---\n%s\n--- rows ---\n%s",
				want, len(got), sql, strings.Join(got, "\n"))
		}
		return nil
	})
}
