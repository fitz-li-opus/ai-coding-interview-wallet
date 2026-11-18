// Package config reads the service's settings from the environment once, at
// startup, and fails there rather than on the first request that needs a value
// nobody supplied.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is everything this process needs to know.
type Config struct {
	// Addr is always 0.0.0.0:PORT. Under Docker the provider reaches this
	// service as host.docker.internal:8080, and a service bound to loopback is
	// a service the webhooks never arrive at.
	Addr string

	DatabaseURL string

	// WebhookSecret is the shared secret the provider signs with.
	WebhookSecret string

	// WebhookTolerance is how old a signature timestamp may be. The provider
	// re-signs on every delivery attempt and its retry window is 31 seconds, so
	// 300 seconds refuses an offline replay without ever refusing a retry.
	WebhookTolerance time.Duration

	// ProviderBaseURL is where the reconciler reads the provider's event list.
	ProviderBaseURL string

	// MigrateOnStart applies the schema before serving. Every statement in it
	// is IF NOT EXISTS, so a second starter is a no-op.
	MigrateOnStart bool
}

// Load reads the environment. The only value with no sensible default is the
// database: a wallet that invents one would come up healthy and lose credits.
func Load() (Config, error) {
	cfg := Config{
		DatabaseURL:     os.Getenv("DATABASE_URL"),
		WebhookSecret:   envOr("WEBHOOK_SECRET", "whsec_interview_test"),
		ProviderBaseURL: strings.TrimRight(envOr("PROVIDER_BASE_URL", "http://localhost:8090"), "/"),
	}

	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}

	port, err := envInt("PORT", 8080)
	if err != nil {
		return Config{}, err
	}
	if port < 1 || port > 65535 {
		return Config{}, fmt.Errorf("PORT out of range: %d", port)
	}
	cfg.Addr = fmt.Sprintf("0.0.0.0:%d", port)

	tolerance, err := envInt("WEBHOOK_TOLERANCE_SECONDS", 300)
	if err != nil {
		return Config{}, err
	}
	if tolerance <= 0 {
		return Config{}, fmt.Errorf("WEBHOOK_TOLERANCE_SECONDS must be positive, got %d", tolerance)
	}
	cfg.WebhookTolerance = time.Duration(tolerance) * time.Second

	cfg.MigrateOnStart, err = envBool("MIGRATE_ON_START", true)
	if err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) (int, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s is not an integer: %q", key, raw)
	}
	return n, nil
}

func envBool(key string, fallback bool) (bool, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s is not a boolean: %q", key, raw)
	}
	return b, nil
}
