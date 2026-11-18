package bdd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

// The images every scenario's stack runs. Both are built once, by BuildImages,
// before any scenario starts: a scenario's stack is then a START rather than a
// COMPILE, which is what makes three containers per scenario affordable.
const (
	walletImage   = "wallet-bdd:latest"
	providerImage = "payment-provider-bdd:latest"

	// Container aliases inside a scenario's own network. The provider reaches
	// the wallet by alias, so neither needs to know a host port.
	aliasPG       = "postgres"
	aliasWallet   = "wallet"
	aliasProvider = "provider"

	// The signing secret both sides are given. It is the interview's value,
	// because the provider's default is the same and a scenario that changed it
	// would be testing a configuration nobody runs.
	webhookSecret = "whsec_interview_test"

	pgUser = "wallet"
	pgPass = "wallet"
	pgDB   = "wallet"
)

// walletModuleDir is the wallet module root — the Docker build context for the
// service image, and where the schema file is read from.
func walletModuleDir() (string, error) {
	// This package lives at <wallet>/test/bdd. WALLET_DIR overrides that, for
	// running these steps from somewhere else.
	rel := "../.."
	if v := os.Getenv("WALLET_DIR"); v != "" {
		rel = v
	}
	abs, err := filepath.Abs(rel)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(abs, "go.mod")); err != nil {
		return "", fmt.Errorf("wallet module root not found at %s: %w", abs, err)
	}
	return abs, nil
}

var buildOnce struct {
	sync.Once
	err error
}

// BuildImages builds the wallet and payment-provider images, once per run.
//
// Building an image is the only thing this suite does to Docker directly;
// everything with a lifecycle is testcontainers' business. The provider image
// is built from services/payment-provider, unmodified — the README forbids
// changing it, and this suite only ever configures it through its environment.
func BuildImages(ctx context.Context) error {
	buildOnce.Do(func() {
		buildOnce.err = func() error {
			walletDir, err := walletModuleDir()
			if err != nil {
				return err
			}
			providerDir := filepath.Join(walletDir, "..", "services", "payment-provider")
			if _, err := os.Stat(filepath.Join(providerDir, "Dockerfile")); err != nil {
				return fmt.Errorf("payment provider build context not found: %w", err)
			}

			provider, err := testcontainers.NewDockerProvider()
			if err != nil {
				return fmt.Errorf("docker provider: %w", err)
			}
			defer provider.Close()

			for _, img := range []struct {
				dir, repo, tag string
			}{
				{walletDir, "wallet-bdd", "latest"},
				{providerDir, "payment-provider-bdd", "latest"},
			} {
				req := testcontainers.ContainerRequest{
					FromDockerfile: testcontainers.FromDockerfile{
						Context:       img.dir,
						Dockerfile:    "Dockerfile",
						Repo:          img.repo,
						Tag:           img.tag,
						KeepImage:     true,
						PrintBuildLog: os.Getenv("BDD_BUILD_LOG") != "",
					},
				}
				if _, err := provider.BuildImage(ctx, &req); err != nil {
					return fmt.Errorf("build %s:%s from %s: %w", img.repo, img.tag, img.dir, err)
				}
			}
			return nil
		}()
	})
	return buildOnce.err
}

// StackOpts is the only thing a scenario may vary about its environment.
type StackOpts struct {
	// DuplicateRate is the provider's DUPLICATE_RATE. It is "0" everywhere, so
	// that a duplicate is something a step causes rather than something a dice
	// roll causes, and "1" for the one scenario tagged @stack:duplicating.
	DuplicateRate string
}

// Stack is one scenario's own containers: postgres, the wallet service, and the
// real payment provider from services/payment-provider — unmodified, configured
// only through its environment.
type Stack struct {
	WalletURL     string // http://host:port of the wallet service
	ProviderURL   string // http://host:port of the payment provider
	PGDSN         string // connection string for this scenario's postgres
	WebhookSecret string // the shared signing secret both sides were given

	// OnPostgresRestarted is called after postgres comes back, because a
	// restarted container is published on a new host port and every pool
	// pointing at the old one is now pointing at nothing. main_test.go sets
	// this to reopen the scenario's pool, which is why no step has to know.
	OnPostgresRestarted func(dsn string) error

	net       *testcontainers.DockerNetwork
	pg        testcontainers.Container
	wallet    testcontainers.Container
	provider  testcontainers.Container
	paused    map[string]bool
	mu        sync.Mutex
	terminate []func(context.Context) error
}

// StartStack brings up one scenario's containers and returns once every one of
// them is ready. Lifecycle is testcontainers' job: no docker run, no manual
// port allocation, no sleep-then-poll, no label sweeping.
func StartStack(ctx context.Context, opts StackOpts) (*Stack, error) {
	if opts.DuplicateRate == "" {
		opts.DuplicateRate = "0"
	}

	s := &Stack{WebhookSecret: webhookSecret, paused: map[string]bool{}}

	net, err := network.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("create network: %w", err)
	}
	s.net = net
	s.terminate = append(s.terminate, func(ctx context.Context) error { return net.Remove(ctx) })

	pg, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:          "postgres:17-alpine",
			ExposedPorts:   []string{"5432/tcp"},
			Networks:       []string{net.Name},
			NetworkAliases: map[string][]string{net.Name: {aliasPG}},
			Env: map[string]string{
				"POSTGRES_USER":     pgUser,
				"POSTGRES_PASSWORD": pgPass,
				"POSTGRES_DB":       pgDB,
			},
			WaitingFor: wait.ForAll(
				wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
				wait.ForListeningPort("5432/tcp"),
			).WithDeadline(90 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		_ = s.Terminate(ctx)
		return nil, fmt.Errorf("start postgres: %w", err)
	}
	s.pg = pg
	s.terminate = append(s.terminate, func(ctx context.Context) error { return pg.Terminate(ctx) })
	if err := s.refreshPGDSN(ctx); err != nil {
		_ = s.Terminate(ctx)
		return nil, err
	}

	// The provider is started before the wallet so that the wallet's first
	// reconciliation has something to talk to, and it reaches the wallet by
	// network alias, so neither start order nor host ports matter to it.
	prov, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:          providerImage,
			ExposedPorts:   []string{"8090/tcp"},
			Networks:       []string{net.Name},
			NetworkAliases: map[string][]string{net.Name: {aliasProvider}},
			Env: map[string]string{
				"PORT":           "8090",
				"WEBHOOK_URL":    fmt.Sprintf("http://%s:8080/webhooks/payments", aliasWallet),
				"WEBHOOK_SECRET": webhookSecret,
				"DUPLICATE_RATE": opts.DuplicateRate,
			},
			WaitingFor: wait.ForHTTP("/health").WithPort("8090/tcp").
				WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		_ = s.Terminate(ctx)
		return nil, fmt.Errorf("start payment provider: %w", err)
	}
	s.provider = prov
	s.terminate = append(s.terminate, func(ctx context.Context) error { return prov.Terminate(ctx) })
	if s.ProviderURL, err = httpURL(ctx, prov, "8090/tcp"); err != nil {
		_ = s.Terminate(ctx)
		return nil, err
	}

	wal, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:          walletImage,
			ExposedPorts:   []string{"8080/tcp"},
			Networks:       []string{net.Name},
			NetworkAliases: map[string][]string{net.Name: {aliasWallet}},
			Env: map[string]string{
				"PORT":              "8080",
				"DATABASE_URL":      fmt.Sprintf("postgres://%s:%s@%s:5432/%s?sslmode=disable", pgUser, pgPass, aliasPG, pgDB),
				"WEBHOOK_SECRET":    webhookSecret,
				"MIGRATE_ON_START":  "true",
				"PROVIDER_BASE_URL": fmt.Sprintf("http://%s:8090", aliasProvider),
			},
			WaitingFor: wait.ForHTTP("/health").WithPort("8080/tcp").
				WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		_ = s.Terminate(ctx)
		return nil, fmt.Errorf("start wallet: %w", err)
	}
	s.wallet = wal
	s.terminate = append(s.terminate, func(ctx context.Context) error { return wal.Terminate(ctx) })
	if s.WalletURL, err = httpURL(ctx, wal, "8080/tcp"); err != nil {
		_ = s.Terminate(ctx)
		return nil, err
	}

	return s, nil
}

func httpURL(ctx context.Context, c testcontainers.Container, port string) (string, error) {
	endpoint, err := c.PortEndpoint(ctx, port, "http")
	if err != nil {
		return "", fmt.Errorf("endpoint for %s: %w", port, err)
	}
	return endpoint, nil
}

func (s *Stack) refreshPGDSN(ctx context.Context) error {
	host, err := s.pg.Host(ctx)
	if err != nil {
		return fmt.Errorf("postgres host: %w", err)
	}
	mapped, err := s.pg.PortEndpoint(ctx, "5432/tcp", "")
	if err != nil {
		return fmt.Errorf("postgres mapped port: %w", err)
	}
	_ = host
	s.PGDSN = fmt.Sprintf("postgres://%s:%s@%s/%s?sslmode=disable", pgUser, pgPass, mapped, pgDB)
	return nil
}

// Terminate removes everything StartStack created.
func (s *Stack) Terminate(ctx context.Context) error {
	var firstErr error
	for i := len(s.terminate) - 1; i >= 0; i-- {
		if err := s.terminate[i](ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	s.terminate = nil
	return firstErr
}

func (s *Stack) container(name string) (testcontainers.Container, error) {
	switch name {
	case "wallet":
		return s.wallet, nil
	case "postgres":
		return s.pg, nil
	case "payment-provider":
		return s.provider, nil
	default:
		return nil, fmt.Errorf("no such service in this stack: %q", name)
	}
}

// StopService stops one named container: "wallet", "postgres" or
// "payment-provider".
//
// For "payment-provider" this SUSPENDS the process and keeps its memory — its
// events and their delivery attempts live only in that process, so a recreated
// container is a provider that never heard of this scenario, and
// reconcile.feature becomes inexpressible. Pause, do not kill.
func (s *Stack) StopService(ctx context.Context, name string) error {
	c, err := s.container(name)
	if err != nil {
		return err
	}

	if name == "payment-provider" {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.paused[name] {
			return nil
		}
		if err := pauseContainer(ctx, c.GetContainerID(), true); err != nil {
			return fmt.Errorf("pause payment provider: %w", err)
		}
		s.paused[name] = true
		return nil
	}

	timeout := 10 * time.Second
	if err := c.Stop(ctx, &timeout); err != nil {
		return fmt.Errorf("stop %s: %w", name, err)
	}
	return nil
}

// StartService starts a container StopService stopped, and returns only once it
// is serving again.
func (s *Stack) StartService(ctx context.Context, name string) error {
	c, err := s.container(name)
	if err != nil {
		return err
	}

	if name == "payment-provider" {
		s.mu.Lock()
		defer s.mu.Unlock()
		if !s.paused[name] {
			return nil
		}
		if err := pauseContainer(ctx, c.GetContainerID(), false); err != nil {
			return fmt.Errorf("unpause payment provider: %w", err)
		}
		s.paused[name] = false
		// An unpaused process is already listening: it never stopped.
		return nil
	}

	if err := c.Start(ctx); err != nil {
		return fmt.Errorf("start %s: %w", name, err)
	}

	// A restarted container is published on a new host port, so every URL and
	// DSN pointing at the old one has to be re-read before anything is told it
	// is ready.
	switch name {
	case "wallet":
		if err := wait.ForHTTP("/health").WithPort("8080/tcp").
			WithStartupTimeout(60*time.Second).WaitUntilReady(ctx, c); err != nil {
			return fmt.Errorf("wallet did not become ready again: %w", err)
		}
		if s.WalletURL, err = httpURL(ctx, c, "8080/tcp"); err != nil {
			return err
		}
	case "postgres":
		if err := wait.ForAll(
			wait.ForLog("database system is ready to accept connections").WithOccurrence(1),
			wait.ForListeningPort("5432/tcp"),
		).WithDeadline(90*time.Second).WaitUntilReady(ctx, c); err != nil {
			return fmt.Errorf("postgres did not become ready again: %w", err)
		}
		if err := s.refreshPGDSN(ctx); err != nil {
			return err
		}
		if s.OnPostgresRestarted != nil {
			if err := s.OnPostgresRestarted(s.PGDSN); err != nil {
				return fmt.Errorf("reconnect to restarted postgres: %w", err)
			}
		}
	}
	return nil
}

// ExecReconciler runs the one-shot reconciler inside the wallet container and
// returns its exit code and output. It is a maintenance command, not an
// endpoint, which is why it is run the way an operator would run it.
func (s *Stack) ExecReconciler(ctx context.Context) (int, string, error) {
	code, reader, err := s.wallet.Exec(ctx, []string{"/usr/local/bin/reconcile"})
	if err != nil {
		return code, "", fmt.Errorf("exec reconciler: %w", err)
	}
	out, err := io.ReadAll(reader)
	if err != nil {
		return code, "", fmt.Errorf("read reconciler output: %w", err)
	}
	return code, string(out), nil
}

// DeliveryAttempt is one attempt the provider made to deliver one event. Status
// is an HTTP status code, or a string when the attempt never got an answer.
type DeliveryAttempt struct {
	At     int64 `json:"at"`
	Status any   `json:"status"`
}

// Accepted reports whether the provider counted this attempt as delivered,
// which is the only thing that stops it from trying again: a 2xx, and nothing
// else.
func (a DeliveryAttempt) Accepted() bool {
	code, ok := a.Status.(float64)
	if !ok {
		if n, isNum := a.Status.(json.Number); isNum {
			f, err := n.Float64()
			if err != nil {
				return false
			}
			code = f
		} else {
			return false
		}
	}
	return code >= 200 && code < 300
}

// ProviderEvent is one event as the provider itself reports it, including the
// attempts it made. This is the counterparty's record, and it is the only
// admissible source for how many times it called us and what it thought of the
// answer.
type ProviderEvent struct {
	ID       string            `json:"id"`
	Type     string            `json:"type"`
	Created  int64             `json:"created"`
	Attempts []DeliveryAttempt `json:"_attempts"`
	Data     struct {
		Payment struct {
			ID          string `json:"id"`
			UserID      string `json:"user_id"`
			AmountCents int64  `json:"amount_cents"`
			Credits     int64  `json:"credits"`
			Status      string `json:"status"`
		} `json:"payment"`
	} `json:"data"`
}

// ProviderEvents reads the provider's event list.
func (s *Stack) ProviderEvents(ctx context.Context) ([]ProviderEvent, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.ProviderURL+"/events", nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("read provider events: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("provider GET /events answered %d: %s", resp.StatusCode, body)
	}
	var payload struct {
		Data []ProviderEvent `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode provider events: %w (body: %s)", err, body)
	}
	return payload.Data, nil
}
