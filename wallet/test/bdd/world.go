// Package bdd is the step implementations for wallet/spec/bdd.
//
// The division of labour this file exists to enforce:
//
//   - A scenario asserts; a step performs. Nothing in a .feature file decides
//     anything, so everything that decides lives here and is shared.
//   - Facts about this service are read from its HTTP answers and from its
//     PostgreSQL rows. Facts about the counterparty are read from the
//     counterparty's own records, never inferred from our tables.
//   - The happy path goes through the real payment provider. The harness only
//     produces what the provider never would: an unsigned webhook, one signed
//     with the wrong secret, one tampered after signing, a stale timestamp, a
//     malformed header or body, and a deliberate ordering.
package bdd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// httpClient is shared by every step that speaks HTTP. Its timeout is longer
// than the provider's own 5-second delivery timeout, so a slow answer is a slow
// answer rather than a client that gave up first.
var httpClient = &http.Client{Timeout: 20 * time.Second}

// Response is one HTTP answer, kept whole so a later step can assert on its
// status, its body and its headers without the step that made it knowing which.
type Response struct {
	Status int
	Body   []byte
	Header http.Header
}

// World is the state of one scenario. A fresh one is built per scenario, and so
// is everything it points at.
type World struct {
	Stack *Stack
	PG    *pgxpool.Pool

	// Vars holds context variables saved by steps that `produce` one, keyed
	// without braces: Vars["payment"] = "pay_1a2b...".
	Vars map[string]string

	// Last is the single recorded response; Set is the set recorded by
	// `these things happen at one instant:`. Exactly one of them is in play at
	// a time — recording either clears the other, because `response status is`
	// reads one and `exactly N responses are` reads the other.
	Last *Response
	Set  []*Response

	// Events are the provider event ids this scenario has learned about, in the
	// order it learned them.
	Events []string
}

// NewWorld returns an empty world bound to a started stack.
func NewWorld(stack *Stack, pg *pgxpool.Pool) *World {
	return &World{Stack: stack, PG: pg, Vars: map[string]string{}}
}

// contextVar matches a {name} reference. The name must start with a letter and
// hold no spaces, which is what keeps this from matching JSON's own braces: an
// object is `{ "user_id": … }` or `{}`, and neither is a reference.
var contextVar = regexp.MustCompile(`\{([a-zA-Z][a-zA-Z0-9_]*)\}`)

// Subst replaces every {name} occurrence — in a path, inside a JSON string, or
// inside a SQL literal — with the context variable of that name.
//
// A name that was never saved is an error rather than a value left as it was.
// Leaving it would turn a scenario that meant `payment_id = 'pay_17…'` into one
// that asserts `payment_id = '{payment}'`, which is a row that never exists and
// an assertion that passes by being wrong.
func (w *World) Subst(s string) (string, error) {
	var missing []string
	out := contextVar.ReplaceAllStringFunc(s, func(match string) string {
		name := match[1 : len(match)-1]
		if v, ok := w.Vars[name]; ok {
			return v
		}
		missing = append(missing, name)
		return match
	})
	if len(missing) > 0 {
		sort.Strings(missing)
		return "", fmt.Errorf("no context variable %s has been saved in this scenario (saved: %s)",
			strings.Join(missing, ", "), w.savedNames())
	}
	return out, nil
}

func (w *World) savedNames() string {
	if len(w.Vars) == 0 {
		return "none"
	}
	names := make([]string, 0, len(w.Vars))
	for k := range w.Vars {
		names = append(names, k)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// Do performs one request against the wallet service and returns its answer.
// It does not record it: the caller decides whether this response is the single
// recorded one or a member of a set.
func (w *World) Do(ctx context.Context, method, path string, headers map[string]string, body []byte) (*Response, error) {
	if w.Stack == nil {
		return nil, fmt.Errorf("this scenario has no stack")
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, w.Stack.WalletURL+path, reader)
	if err != nil {
		return nil, fmt.Errorf("build %s %s: %w", method, path, err)
	}
	if body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body of %s %s: %w", method, path, err)
	}
	return &Response{Status: resp.StatusCode, Body: payload, Header: resp.Header.Clone()}, nil
}

// Record makes r the single recorded response and clears any recorded set.
func (w *World) Record(r *Response) {
	w.Last = r
	w.Set = nil
}

// RecordSet makes rs the recorded set and clears the single recorded response.
func (w *World) RecordSet(rs []*Response) {
	w.Set = rs
	w.Last = nil
}

// Reconnect points this world's pool at a new DSN, after the container behind
// the old one was restarted on a different host port.
func (w *World) Reconnect(ctx context.Context, dsn string) error {
	pool, err := openPool(ctx, dsn)
	if err != nil {
		return err
	}
	if w.PG != nil {
		w.PG.Close()
	}
	w.PG = pool
	return nil
}
