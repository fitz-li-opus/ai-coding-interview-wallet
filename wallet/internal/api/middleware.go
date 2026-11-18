package api

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

type ctxKey int

const ctxKeyRequestID ctxKey = iota

const (
	headerRequestID = "X-Request-Id"
	maxRequestIDLen = 200
)

// withRequestID gives every response a request id: the client's own if it sent
// one, so a trace that starts in the caller does not break here, and a
// generated one otherwise, so there is no response that cannot be found in a
// log. The header is set before the handler runs, so a refusal carries it too —
// those are the responses somebody actually goes looking for.
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := sanitizeRequestID(r.Header.Get(headerRequestID))
		if id == "" {
			id = newRequestID()
		}
		w.Header().Set(headerRequestID, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKeyRequestID, id)))
	})
}

// withLogging writes one line per request. It logs the method, the path, the
// status and how long it took — never a header, so the signature and the
// signing secret cannot reach a log through here.
func withLogging(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		logger.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"bytes", rec.written,
			"duration_ms", time.Since(started).Milliseconds(),
			"request_id", RequestID(r.Context()),
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status  int
	written int
	wrote   bool
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.wrote {
		return
	}
	r.status = status
	r.wrote = true
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wrote {
		r.WriteHeader(http.StatusOK)
	}
	n, err := r.ResponseWriter.Write(b)
	r.written += n
	return n, err
}

// RequestID returns the request id carried by ctx, or "".
func RequestID(ctx context.Context) string {
	if id, ok := ctx.Value(ctxKeyRequestID).(string); ok {
		return id
	}
	return ""
}

// sanitizeRequestID keeps a client's id verbatim apart from control characters
// and an unreasonable length.
func sanitizeRequestID(raw string) string {
	if len(raw) > maxRequestIDLen {
		raw = raw[:maxRequestIDLen]
	}
	out := make([]rune, 0, len(raw))
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			continue
		}
		out = append(out, r)
	}
	return string(out)
}

// newRequestID is a v4 UUID. Its hyphens are the part of the shape worth
// asserting from outside, which is what the scenario for a generated id does.
func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("req-%d-%d", time.Now().UnixNano(), time.Now().Unix())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
