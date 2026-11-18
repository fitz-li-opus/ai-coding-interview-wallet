// Package provider is a client for the payment provider's own API — the
// counterparty, not this service.
//
// It is transport only: it knows the wire shape of `GET /events` and nothing
// about what an event means. Deciding which events matter is reconciliation's
// job, and crediting a wallet is ingest's; this package exists so that neither
// of them has to contain an HTTP call.
package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// These two errors are kept apart because the caller can act on the
// difference: unreachable is worth retrying later, an unusable response is
// not, and a reconciliation pass that reports "the provider is down" is a
// different operational fact from one that reports "the provider answered
// something I cannot read".
var (
	// ErrUnreachable: no answer at all — connection refused, DNS, timeout.
	ErrUnreachable = errors.New("payment provider unreachable")
	// ErrBadResponse: an answer arrived and could not be used.
	ErrBadResponse = errors.New("payment provider returned an unusable response")
)

// defaultTimeout bounds one call. The provider is a single-threaded-ish mock on
// the same network; a call that takes longer than this is a failure, not a slow
// success, and a reconciler that hangs is worse than one that reports an error.
const defaultTimeout = 10 * time.Second

// Payment is the provider's payment object as it appears inside an event.
// It is a copy of the wire shape on purpose: this package must not import the
// one that applies events, or the dependency runs the wrong way.
type Payment struct {
	ID          string `json:"id"`
	UserID      string `json:"user_id"`
	AmountCents int64  `json:"amount_cents"`
	Credits     int64  `json:"credits"`
	Status      string `json:"status"`
}

// AttemptStatus is what one delivery attempt produced. The provider writes an
// HTTP status as a number, and a failure with no answer at all as a string
// (`error: URLError`) — a stopped wallet looks like the second. Both have to
// decode, so neither an int nor a string field would do.
type AttemptStatus struct {
	// Code is the HTTP status, or 0 when there was no answer.
	Code int
	// Text is the provider's own words when there was no answer, empty
	// otherwise.
	Text string
}

// UnmarshalJSON accepts a number or a string.
func (s *AttemptStatus) UnmarshalJSON(b []byte) error {
	var code int
	if err := json.Unmarshal(b, &code); err == nil {
		s.Code, s.Text = code, ""
		return nil
	}
	var text string
	if err := json.Unmarshal(b, &text); err == nil {
		s.Code, s.Text = 0, text
		return nil
	}
	return fmt.Errorf("%w: delivery attempt status %s is neither a number nor a string", ErrBadResponse, b)
}

// DeliveryAttempt is one attempt, as the provider logged it.
type DeliveryAttempt struct {
	At     int64         `json:"at"`
	Status AttemptStatus `json:"status"`
}

// Event is one event as `GET /events` reports it, including the provider's own
// delivery log. The leading underscore of `_attempts` is the provider's: it
// marks the field as the mock's bookkeeping rather than part of the event.
type Event struct {
	ID       string            `json:"id"`
	Type     string            `json:"type"`
	Created  int64             `json:"created"`
	Attempts []DeliveryAttempt `json:"_attempts"`
	Data     struct {
		Payment Payment `json:"payment"`
	} `json:"data"`
}

// Client talks to one payment provider.
type Client struct {
	baseURL string
	http    *http.Client
}

// New returns a client for the provider at baseURL (for example
// "http://localhost:8090").
func New(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: defaultTimeout},
	}
}

// Events returns every event the provider remembers, oldest first.
//
// This is the only question the provider can be asked about what happened:
// there is no endpoint for a single payment, which is exactly why
// reconciliation is written against this list.
func (c *Client) Events(ctx context.Context) ([]Event, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/events", nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: GET %s/events: %v", ErrUnreachable, c.baseURL, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: reading GET %s/events: %v", ErrUnreachable, c.baseURL, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: GET %s/events answered %d", ErrBadResponse, c.baseURL, resp.StatusCode)
	}

	var payload struct {
		Data []Event `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("%w: GET %s/events: %v", ErrBadResponse, c.baseURL, err)
	}
	return payload.Data, nil
}
