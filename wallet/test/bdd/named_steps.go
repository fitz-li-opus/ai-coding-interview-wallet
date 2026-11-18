package bdd

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
)

// NamedStep resolves one sentence of the registry into the action it performs,
// for `these things happen at one instant:` to park at its gate alongside the
// HTTP callers.
//
// Only the registry's docstring-free `When` steps can be named this way, which
// is exactly what format.yml's step_ref declares:
//
//	payment provider refunds payment "<id>"
//	payment provider redelivers each event <n> times
//	the reconciler runs
//	service "(wallet|postgres|payment-provider)" is (stopped|started)
//
// The returned action must do the work itself and report its own failure; an
// actor that produces no HTTP response contributes nothing to the response set.
// ok is false for a sentence this registry cannot name, which is a step failure
// rather than a silent no-op.
//
// The patterns are the registry's own, and the actions are the same functions
// the ordinary steps call — a named step is the same step, released at a chosen
// instant, not a second implementation of it.
func NamedStep(sentence string, w *World) (action func(ctx context.Context) error, ok bool) {
	if m := namedRefund.FindStringSubmatch(sentence); m != nil {
		return func(ctx context.Context) error {
			id, err := w.Subst(m[1])
			if err != nil {
				return err
			}
			return refundPayment(ctx, w, id)
		}, true
	}

	if m := namedRedeliver.FindStringSubmatch(sentence); m != nil {
		times, err := strconv.Atoi(m[1])
		if err != nil {
			return nil, false
		}
		return func(ctx context.Context) error {
			return redeliverEachEvent(ctx, w, times)
		}, true
	}

	if namedReconciler.MatchString(sentence) {
		return func(ctx context.Context) error {
			return runReconciler(ctx, w)
		}, true
	}

	if m := namedService.FindStringSubmatch(sentence); m != nil {
		return func(ctx context.Context) error {
			return setServiceState(ctx, w, m[1], m[2])
		}, true
	}

	return nil, false
}

// NamedStepError is the failure a caller should report for a sentence this
// registry cannot name. It is here so that the message is the same wherever the
// gate is built.
func NamedStepError(sentence string) error {
	return fmt.Errorf("no step of this registry can be named here: %q\n"+
		"nameable steps take no docstring and are declared `When`: "+
		"a provider refund, a redelivery, the reconciler, a service stop or start",
		sentence)
}

var (
	namedRefund     = regexp.MustCompile(`^payment provider refunds payment "([^"]+)"$`)
	namedRedeliver  = regexp.MustCompile(`^payment provider redelivers each event ([0-9]+) times?$`)
	namedReconciler = regexp.MustCompile(`^the reconciler runs$`)
	namedService    = regexp.MustCompile(`^service "(wallet|postgres|payment-provider)" is (stopped|started)$`)
)
