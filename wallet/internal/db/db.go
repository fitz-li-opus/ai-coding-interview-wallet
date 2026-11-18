// Package db opens the one PostgreSQL pool the service uses and applies the
// schema. It is deliberately the only place that knows how to do either.
package db

import (
	"context"
	"embed"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/0001_init.sql
var schema embed.FS

// SchemaPath is where the single definition of the schema lives, relative to
// the wallet module root. The BDD harness reads this same file.
const SchemaPath = "internal/db/migrations/0001_init.sql"

// Open returns a pool that has proven it can reach the database. It retries
// until ctx is done, because a container that is listening is not yet a
// database that is ready.
func Open(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	cfg.MaxConns = 16
	cfg.MaxConnLifetime = time.Hour

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open pool: %w", err)
	}

	for {
		if err = pool.Ping(ctx); err == nil {
			return pool, nil
		}
		select {
		case <-ctx.Done():
			pool.Close()
			return nil, fmt.Errorf("database never became reachable: %w", err)
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// Schema returns the schema DDL.
func Schema() (string, error) {
	b, err := schema.ReadFile("migrations/0001_init.sql")
	if err != nil {
		return "", fmt.Errorf("read schema: %w", err)
	}
	return string(b), nil
}

// Migrate applies the schema. Every statement is `IF NOT EXISTS`, so a second
// caller against the same database is a no-op rather than a failure.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	sql, err := Schema()
	if err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, sql); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	return nil
}
