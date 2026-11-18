package bdd

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/cucumber/godog"
)

// Operating the deployment from outside it: waiting for the provider and the
// wallet to be done with each other, taking a container away mid-scenario, and
// running the repair pass once.
//
// Nothing in this file cooperates with the service under test. There is no
// test-only endpoint, no injected clock, no flag that makes the wallet behave
// differently while being watched. An outage is a stopped container and a
// settled system is one the provider has stopped calling.

const (
	// settlePollInterval is how often the provider's log is re-read. The
	// registry fixes it at 50 ms.
	settlePollInterval = 50 * time.Millisecond

	// settleTimeout bounds the wait by the provider's own schedule — six
	// attempts at 0, 1, 2, 4, 8 and 16 seconds, each with a 5-second timeout —
	// plus a margin. A wallet that is merely slow fails here, with the log
	// printed, rather than intermittently in the assertion below.
	settleTimeout = 90 * time.Second

	// redeliverTimeout bounds a burst of resends. Each is a single attempt
	// with no retry loop, and a refused one fails as fast as a connection
	// refusal, so this is generous by an order of magnitude.
	redeliverTimeout = 30 * time.Second

	// serviceTimeout bounds stopping or starting one container.
	serviceTimeout = 60 * time.Second
)

// SettleQuietWindow is how long the provider's delivery log must stay unchanged
// before the system counts as settled.
//
// It exists because of DUPLICATE_RATE. With it set to 1 — the @stack:duplicating
// scenario — the provider waits a random 0.1 to 1.5 seconds after a successful
// delivery and then delivers the same event once more, spontaneously. That copy
// is the whole point of that scenario, and a settle that returned as soon as
// the first 2xx appeared would assert the count before it arrived.
//
// The default therefore covers that jitter, which costs every other settle the
// same wait. A stack that sets DUPLICATE_RATE to 0 may lower it — one
// assignment from the code that starts the stack, which knows the rate:
//
//	bdd.SettleQuietWindow = 300 * time.Millisecond
//
// Correct by default, faster by choice. Nothing else in the provider schedules
// an attempt after a 2xx, so a short window is safe once duplicates are off.
var SettleQuietWindow = 1700 * time.Millisecond

// serviceState remembers which of a scenario's containers this suite has
// stopped, so that `service "x" is stopped` can fail when x is already
// stopped. A scenario that thinks it caused an outage and did not is asserting
// nothing, and Docker will not tell us: stopping a stopped container and
// starting a started one both succeed quietly.
var serviceState = struct {
	sync.Mutex
	stopped map[*Stack]map[string]bool
}{stopped: map[*Stack]map[string]bool{}}

// RegisterOpsSteps registers its share of the registry. See steps.go.
func RegisterOpsSteps(sc *godog.ScenarioContext, w *World) {
	sc.Step(`^the reconciler runs$`, func(ctx context.Context) error {
		return runReconciler(ctx, w)
	})

	sc.Step(`^service "(wallet|postgres|payment-provider)" is (stopped|started)$`,
		func(ctx context.Context, service, state string) error {
			return setServiceState(ctx, w, service, state)
		})

	sc.Step(`^background work has settled$`, func(ctx context.Context) error {
		return settle(ctx, w)
	})
}

// runReconciler runs the repair pass once, to completion, inside the wallet
// container. One shot rather than a loop is what lets a scenario say WHEN it
// ran, which is what makes "it changed nothing" an assertion instead of a hope.
func runReconciler(ctx context.Context, w *World) error {
	code, output, err := w.Stack.ExecReconciler(ctx)
	if err != nil {
		return fmt.Errorf("could not run the reconciler: %w\n%s", err, output)
	}
	if code != 0 {
		return fmt.Errorf("the reconciler exited %d:\n%s", code, output)
	}
	return nil
}

// setServiceState stops or starts one container and returns once it is really
// in that state.
func setServiceState(ctx context.Context, w *World, service, state string) error {
	ctx, cancel := context.WithTimeout(ctx, serviceTimeout)
	defer cancel()

	serviceState.Lock()
	stopped := serviceState.stopped[w.Stack]
	if stopped == nil {
		stopped = map[string]bool{}
		serviceState.stopped[w.Stack] = stopped
	}
	already := stopped[service] == (state == "stopped")
	serviceState.Unlock()

	if already {
		return fmt.Errorf("service %q is already %s, so this step would inject no fault", service, state)
	}

	var err error
	if state == "stopped" {
		err = w.Stack.StopService(ctx, service)
	} else {
		err = w.Stack.StartService(ctx, service)
	}
	if err != nil {
		return fmt.Errorf("could not make service %q %s: %w", service, state, err)
	}

	serviceState.Lock()
	serviceState.stopped[w.Stack][service] = state == "stopped"
	serviceState.Unlock()
	return nil
}

// settle waits until nothing is in flight between the provider and the wallet.
// It asserts nothing: it is the condition that makes the NEXT step a statement
// about a settled system rather than about a race.
//
// Two things must hold at once, both read from the provider's own GET /events:
//
//   - every event it created has an attempt whose status was 2xx, or has
//     exhausted its six attempts — the provider gives up after
//     0, 1, 2, 4, 8, 16 seconds and the event is then only repairable by
//     reconciliation, which reconcile.feature depends on being able to reach;
//   - no further attempt is scheduled, judged by the log not changing for
//     SettleQuietWindow, which is what catches the spontaneous duplicate copy
//     DUPLICATE_RATE=1 promises.
func settle(ctx context.Context, w *World) error {
	const providerAttemptBudget = 6

	deadline := time.Now().Add(settleTimeout)
	var (
		lastTotal  = -1
		quietSince time.Time
	)

	for {
		events, err := w.Stack.ProviderEvents(ctx)
		if err != nil {
			return fmt.Errorf("could not read the payment provider's delivery log: %w", err)
		}
		noteEventsFrom(w, events)

		done := true
		for _, ev := range events {
			accepted := false
			for _, a := range ev.Attempts {
				if attemptAccepted(a) {
					accepted = true
					break
				}
			}
			if !accepted && len(ev.Attempts) < providerAttemptBudget {
				done = false
				break
			}
		}

		total := totalAttempts(events)
		if total != lastTotal {
			// The log moved: whatever we thought was quiet was not.
			lastTotal = total
			quietSince = time.Now()
		}

		if done && time.Since(quietSince) >= SettleQuietWindow {
			return nil
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("background work had not settled after %s\n%s",
				settleTimeout, describeAttempts(events))
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(settlePollInterval):
		}
	}
}
