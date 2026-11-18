package bdd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/cucumber/godog"
	"github.com/cucumber/godog/colors"
)

// featureDir is spec/bdd/api — the whole suite, because this project has one
// surface. See the header of spec/bdd/format.yml for why there is no e2e.
func featureDir() (string, error) {
	root, err := walletModuleDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "spec", "bdd", "api"), nil
}

// TestFeatures runs the scenarios of wallet/spec/bdd.
//
//	go test ./...                           every scenario
//	BDD_TAGS=@minimum go test ./...         the README's minimum requirement
//	BDD_TAGS=@spend go test ./...           one feature's tag
//	BDD_FEATURE=credit.feature go test ./... one file
//
// BDD_CONCURRENCY sets how many scenarios run at once; each one owns three
// containers, so the default is deliberately low.
func TestFeatures(t *testing.T) {
	ctx := context.Background()

	// One build for the whole run. A scenario's stack is then a start rather
	// than a compile, which is what makes three containers per scenario
	// affordable.
	if err := BuildImages(ctx); err != nil {
		t.Fatalf("build images: %v", err)
	}

	dir, err := featureDir()
	if err != nil {
		t.Fatalf("locate features: %v", err)
	}
	paths := []string{dir}
	if f := os.Getenv("BDD_FEATURE"); f != "" {
		paths = []string{filepath.Join(dir, f)}
	}

	concurrency := 2
	if v := os.Getenv("BDD_CONCURRENCY"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			t.Fatalf("BDD_CONCURRENCY must be a positive integer, got %q", v)
		}
		concurrency = n
	}

	// BDD_FORMAT picks godog's formatter(s). It accepts godog's own syntax, so
	// one run can write a report and still be readable on the terminal:
	//
	//	BDD_FORMAT='pretty,cucumber:/tmp/run.json'
	//
	// The default stays "pretty" because a human runs this far more often than
	// a pipeline does.
	format := "pretty"
	if v := os.Getenv("BDD_FORMAT"); v != "" {
		format = v
	}

	opts := godog.Options{
		Format:      format,
		Paths:       paths,
		Tags:        os.Getenv("BDD_TAGS"),
		Output:      colors.Colored(os.Stdout),
		Concurrency: concurrency,
		// A step the harness has not implemented fails the scenario. A suite
		// that reports "undefined" as a pass is a suite that reports a
		// specification nobody implemented as a system that works.
		Strict: true,
	}

	status := godog.TestSuite{
		Name:                "wallet",
		ScenarioInitializer: InitializeScenario,
		Options:             &opts,
	}.Run()

	if status != 0 {
		t.Fatalf("scenarios failed (godog exit status %d)", status)
	}
}

// InitializeScenario gives one scenario its own containers, its own database
// and its own world, and takes them all away afterwards.
//
// Nothing is shared between scenarios — not a table, not a container, not a
// port. That is the whole reason a scenario may stop postgres or freeze the
// provider and still be a scenario rather than a disaster.
func InitializeScenario(sc *godog.ScenarioContext) {
	w := &World{Vars: map[string]string{}}
	register(sc, w)

	sc.Before(func(ctx context.Context, scenario *godog.Scenario) (context.Context, error) {
		opts := StackOpts{DuplicateRate: "0"}
		for _, tag := range scenario.Tags {
			switch tag.Name {
			// The provider's own coin flip, pinned to always. The interview's
			// default is 0.3, and a suite that leaves it there asserts its
			// duplicate handling 30% of the time.
			case "@stack:duplicating", "@webhooks:duplicated":
				opts.DuplicateRate = "1"
			}
		}

		stack, err := StartStack(ctx, opts)
		if err != nil {
			return ctx, fmt.Errorf("start stack for %q: %w", scenario.Name, err)
		}
		pool, err := openPool(ctx, stack.PGDSN)
		if err != nil {
			_ = stack.Terminate(ctx)
			return ctx, fmt.Errorf("connect to scenario postgres: %w", err)
		}

		w.Stack = stack
		w.PG = pool
		w.Vars = map[string]string{}
		w.Last = nil
		w.Set = nil
		w.Events = nil

		// A restarted postgres comes back on a different host port, so the
		// pool has to be reopened. The stack tells the world; no step has to
		// know that happened.
		stack.OnPostgresRestarted = func(dsn string) error { return w.Reconnect(ctx, dsn) }

		return ctx, nil
	})

	sc.After(func(ctx context.Context, scenario *godog.Scenario, scenarioErr error) (context.Context, error) {
		if w.PG != nil {
			w.PG.Close()
			w.PG = nil
		}
		if w.Stack != nil {
			if err := w.Stack.Terminate(ctx); err != nil {
				// Not the scenario's failure, but it must not be silent: a
				// leaked container is the next run's mystery.
				fmt.Fprintf(os.Stderr, "terminate stack for %q: %v\n", scenario.Name, err)
			}
			w.Stack = nil
		}
		return ctx, nil
	})
}
