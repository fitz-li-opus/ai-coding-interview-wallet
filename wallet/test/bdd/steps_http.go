package bdd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/cucumber/godog"
)

// requestEnvelope is the docstring of an HTTP step: the headers to send and the
// body to send, and nothing else. `allowed_top_level_keys` in format.yml is
// what keeps it that way, and this struct is the other half of that promise.
type requestEnvelope struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
	Step    string            `json:"step"`
}

func decodeEnvelope(raw string) (requestEnvelope, error) {
	var env requestEnvelope
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&env); err != nil {
		return env, fmt.Errorf("decode request docstring: %w\n--- docstring ---\n%s", err, raw)
	}
	return env, nil
}

// bodyBytes returns the body to send: nil when the docstring carried none, so
// that a GET with `{}` is a GET with no body rather than a GET with "null".
func (e requestEnvelope) bodyBytes() []byte {
	if len(e.Body) == 0 {
		return nil
	}
	return e.Body
}

// RegisterHTTPSteps registers its share of the registry. See steps.go.
func RegisterHTTPSteps(sc *godog.ScenarioContext, w *World) {
	sc.Step(`^(GET|POST|PUT|PATCH|DELETE) (/[a-zA-Z0-9/{}:._?=&%-]*):$`,
		func(ctx context.Context, method, path string, doc *godog.DocString) error {
			resp, err := w.request(ctx, method, path, doc.Content)
			if err != nil {
				return err
			}
			w.Record(resp)
			return nil
		})

	sc.Step(`^these things happen at one instant:$`, func(ctx context.Context, doc *godog.DocString) error {
		return w.atOneInstant(ctx, doc.Content)
	})

	sc.Step(`^response status is ([1-5][0-9]{2})$`, func(want int) error {
		if w.Last == nil {
			return fmt.Errorf("no single response has been recorded (a race records a set; assert it with `exactly <n> responses are <status>`)")
		}
		if w.Last.Status != want {
			return fmt.Errorf("expected status %d, got %d\n--- body ---\n%s", want, w.Last.Status, w.Last.Body)
		}
		return nil
	})

	sc.Step(`^exactly ([0-9]+) responses? (?:is|are) ([1-5][0-9]{2})$`, func(wantCount, wantStatus int) error {
		if w.Set == nil {
			return fmt.Errorf("no set of responses has been recorded (this reads what `these things happen at one instant:` produced)")
		}
		got := 0
		var summary []string
		for _, r := range w.Set {
			if r.Status == wantStatus {
				got++
			}
			summary = append(summary, fmt.Sprintf("%d %s", r.Status, bytes.TrimSpace(r.Body)))
		}
		if got != wantCount {
			return fmt.Errorf("expected exactly %d response(s) with status %d, got %d\n--- responses ---\n%s",
				wantCount, wantStatus, got, strings.Join(summary, "\n"))
		}
		return nil
	})

	sc.Step(`^response body contains:$`, func(doc *godog.DocString) error {
		if w.Last == nil {
			return fmt.Errorf("no single response has been recorded")
		}
		expectedRaw, err := w.Subst(doc.Content)
		if err != nil {
			return err
		}
		expected, err := decodeJSON([]byte(expectedRaw))
		if err != nil {
			return fmt.Errorf("expected body is not JSON: %w", err)
		}
		actual, err := decodeJSON(w.Last.Body)
		if err != nil {
			return fmt.Errorf("response body is not JSON: %w\n--- body ---\n%s", err, w.Last.Body)
		}
		if err := jsonSubset(expected, actual, "$"); err != nil {
			return fmt.Errorf("%w\n--- expected (subset) ---\n%s\n--- actual ---\n%s", err, expectedRaw, w.Last.Body)
		}
		return nil
	})

	sc.Step(`^response body does not contain "([^"]+)"$`, func(text string) error {
		if w.Last == nil {
			return fmt.Errorf("no single response has been recorded")
		}
		if bytes.Contains(w.Last.Body, []byte(text)) {
			return fmt.Errorf("response body contains %q and must not\n--- body ---\n%s", text, w.Last.Body)
		}
		return nil
	})

	sc.Step(`^response header "([^"]+)" contains "([^"]+)"$`, func(name, want string) error {
		if w.Last == nil {
			return fmt.Errorf("no single response has been recorded")
		}
		want, err := w.Subst(want)
		if err != nil {
			return err
		}
		got := w.Last.Header.Get(name)
		if got == "" {
			return fmt.Errorf("response has no %s header\n--- headers ---\n%v", name, w.Last.Header)
		}
		if !strings.Contains(got, want) {
			return fmt.Errorf("header %s is %q, which does not contain %q", name, got, want)
		}
		return nil
	})

	sc.Step(`^save response body field "([a-zA-Z_][a-zA-Z0-9_]*)" as "([a-z][a-zA-Z0-9]+)"$`,
		func(field, name string) error {
			if w.Last == nil {
				return fmt.Errorf("no single response has been recorded")
			}
			actual, err := decodeJSON(w.Last.Body)
			if err != nil {
				return fmt.Errorf("response body is not JSON: %w", err)
			}
			obj, ok := actual.(map[string]any)
			if !ok {
				return fmt.Errorf("response body is not a JSON object")
			}
			value, ok := obj[field]
			if !ok {
				return fmt.Errorf("response body has no field %q\n--- body ---\n%s", field, w.Last.Body)
			}
			if value == nil {
				return fmt.Errorf("field %q is null, which is not a value worth saving", field)
			}
			w.Vars[name] = scalarString(value)
			return nil
		})
}

// request performs one HTTP step: substitute the path and the docstring, then
// call the wallet.
func (w *World) request(ctx context.Context, method, path, docstring string) (*Response, error) {
	path, err := w.Subst(path)
	if err != nil {
		return nil, err
	}
	raw, err := w.Subst(docstring)
	if err != nil {
		return nil, err
	}
	env, err := decodeEnvelope(raw)
	if err != nil {
		return nil, err
	}
	if env.Method != "" || env.Path != "" || env.Step != "" {
		return nil, fmt.Errorf("an HTTP step's docstring carries only headers and body; method, path and step belong to `these things happen at one instant:`")
	}
	return w.Do(ctx, method, path, env.Headers, env.bodyBytes())
}

// atOneInstant builds every actor, parks them all at one gate, releases them
// together, and records what the callers answered as a set.
//
// An actor is either an envelope — a caller of this service, naming its own
// method and path — or `{"step": "…"}`, which is any other `When` step of this
// registry that takes no docstring. The second form is the only way to race the
// actors that are not callers: a redelivery, a refund at the provider, a
// reconciliation pass, a container going away.
func (w *World) atOneInstant(ctx context.Context, docstring string) error {
	raw, err := w.Subst(docstring)
	if err != nil {
		return err
	}
	var actors []requestEnvelope
	if err := json.Unmarshal([]byte(raw), &actors); err != nil {
		return fmt.Errorf("decode actors: %w\n--- docstring ---\n%s", err, raw)
	}
	if len(actors) < 2 {
		return fmt.Errorf("a race needs at least two actors, got %d", len(actors))
	}

	type outcome struct {
		resp *Response
		err  error
	}
	results := make([]outcome, len(actors))
	gate := make(chan struct{})
	var wg sync.WaitGroup

	for i, actor := range actors {
		switch {
		case actor.Step != "":
			action, ok := NamedStep(actor.Step, w)
			if !ok {
				return fmt.Errorf("actor %d names a step this registry cannot perform without a docstring: %q", i, actor.Step)
			}
			wg.Add(1)
			go func(i int, action func(context.Context) error) {
				defer wg.Done()
				<-gate
				results[i] = outcome{err: action(ctx)}
			}(i, action)
		case actor.Method != "" && actor.Path != "":
			wg.Add(1)
			go func(i int, a requestEnvelope) {
				defer wg.Done()
				<-gate
				resp, err := w.Do(ctx, a.Method, a.Path, a.Headers, a.bodyBytes())
				results[i] = outcome{resp: resp, err: err}
			}(i, actor)
		default:
			return fmt.Errorf("actor %d is neither an envelope with a method and a path nor a named step", i)
		}
	}

	close(gate)
	wg.Wait()

	var set []*Response
	var failures []string
	for i, r := range results {
		if r.err != nil {
			failures = append(failures, fmt.Sprintf("actor %d: %v", i, r.err))
			continue
		}
		// An actor that produces no response contributes nothing to the set:
		// a reconciliation pass is not an answer with a status code.
		if r.resp != nil {
			set = append(set, r.resp)
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("actors failed:\n%s", strings.Join(failures, "\n"))
	}
	w.RecordSet(set)
	return nil
}

// decodeJSON decodes with numbers left as written. A balance is an integer and
// a float64 round-trip is how an integer stops being one.
func decodeJSON(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

func scalarString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case bool:
		return fmt.Sprintf("%t", t)
	default:
		return fmt.Sprint(t)
	}
}

// jsonSubset asserts that `expected` is present in `actual`: every key the
// docstring names, with the value it names. Keys the docstring does not mention
// are not the scenario's business.
//
// Two conventions from format.yml are honoured here:
//
//	"<non-null>"  the key must be present and must not be null
//	"500"         a quoted scalar matches the unquoted one, because a Scenario
//	              Outline substitutes `<amount>` into a JSON string position
//	              and the service answers with a number
func jsonSubset(expected, actual any, path string) error {
	switch want := expected.(type) {
	case map[string]any:
		got, ok := actual.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: expected an object, got %T", path, actual)
		}
		for key, wantValue := range want {
			gotValue, present := got[key]
			if !present {
				return fmt.Errorf("%s.%s is missing", path, key)
			}
			if err := jsonSubset(wantValue, gotValue, path+"."+key); err != nil {
				return err
			}
		}
		return nil

	case []any:
		got, ok := actual.([]any)
		if !ok {
			return fmt.Errorf("%s: expected an array, got %T", path, actual)
		}
		if len(want) != len(got) {
			return fmt.Errorf("%s: expected %d element(s), got %d", path, len(want), len(got))
		}
		for i := range want {
			if err := jsonSubset(want[i], got[i], fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
		return nil

	case string:
		if want == "<non-null>" {
			if actual == nil {
				return fmt.Errorf("%s is null, expected a value", path)
			}
			return nil
		}
		if actual == nil {
			return fmt.Errorf("%s is null, expected %q", path, want)
		}
		if got := scalarString(actual); got != want {
			return fmt.Errorf("%s is %s, expected %s", path, quoteFor(actual, got), quoteFor(want, want))
		}
		return nil

	case nil:
		if actual != nil {
			return fmt.Errorf("%s is %v, expected null", path, actual)
		}
		return nil

	default:
		if scalarString(expected) != scalarString(actual) {
			return fmt.Errorf("%s is %v, expected %v", path, actual, expected)
		}
		return nil
	}
}

func quoteFor(v any, s string) string {
	if _, isString := v.(string); isString {
		return fmt.Sprintf("%q", s)
	}
	return s
}
