package etoro

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

const (
	testAPIKey  = "test-api-key-not-real"
	testUserKey = "test-user-key-do-not-leak"
)

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// testClient returns a Client wired to a httptest server with a tiny retry
// backoff so 429 tests stay fast. Extra options (for example Real) are
// applied after the server wiring.
func testClient(srv *httptest.Server, opts ...Option) *Client {
	c := New(testAPIKey, testUserKey, append([]Option{WithHTTPClient(srv.Client())}, opts...)...)
	c.baseURL = srv.URL
	c.backoff = time.Millisecond
	return c
}

func TestAuthHeadersOnEveryRequest(t *testing.T) {
	type captured struct{ apiKey, userKey, requestID string }
	var got []captured
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, captured{
			apiKey:    r.Header.Get("x-api-key"),
			userKey:   r.Header.Get("x-user-key"),
			requestID: r.Header.Get("x-request-id"),
		})
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := testClient(srv)
	var sentIDs []string
	for i := 0; i < 2; i++ {
		_, id, err := c.do(t.Context(), http.MethodGet, "/api/v1/me", nil, nil)
		if err != nil {
			t.Fatalf("do call %d: %v", i+1, err)
		}
		sentIDs = append(sentIDs, id)
	}
	if len(got) != 2 {
		t.Fatalf("server saw %d requests, want 2", len(got))
	}
	for i, g := range got {
		if g.apiKey != testAPIKey {
			t.Errorf("request %d: x-api-key = %q, want %q", i+1, g.apiKey, testAPIKey)
		}
		if g.userKey != testUserKey {
			t.Errorf("request %d: x-user-key = %q, want %q", i+1, g.userKey, testUserKey)
		}
		if !uuidV4.MatchString(g.requestID) {
			t.Errorf("request %d: x-request-id = %q, not a canonical UUID v4", i+1, g.requestID)
		}
		if g.requestID != sentIDs[i] {
			t.Errorf("request %d: header id %q != returned id %q", i+1, g.requestID, sentIDs[i])
		}
	}
	if got[0].requestID == got[1].requestID {
		t.Errorf("x-request-id repeated across calls: %q", got[0].requestID)
	}
}

func TestModeSelection(t *testing.T) {
	const (
		demoPath = "/api/v1/trading/info/demo/pnl"
		realPath = "/api/v1/trading/info/real/pnl"
	)
	if c := New(testAPIKey, testUserKey); c.modePath(demoPath, realPath) != demoPath {
		t.Error("default mode should be demo")
	}
	if c := New(testAPIKey, testUserKey, Real()); c.modePath(demoPath, realPath) != realPath {
		t.Error("Real() should select the real path")
	}
	if c := New(testAPIKey, testUserKey, Real(), Demo()); c.modePath(demoPath, realPath) != demoPath {
		t.Error("Demo() should select the demo path")
	}
	if c := New(testAPIKey, testUserKey); c.baseURL != host {
		t.Errorf("default baseURL = %q, want %q", c.baseURL, host)
	}
}

func TestRetryOn429ReusesRequestID(t *testing.T) {
	var ids []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ids = append(ids, r.Header.Get("x-request-id"))
		if len(ids) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	if _, _, err := testClient(srv).do(t.Context(), http.MethodGet, "/x", nil, nil); err != nil {
		t.Fatalf("do after one 429: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("calls = %d, want 2", len(ids))
	}
	if ids[0] != ids[1] {
		t.Errorf("retry changed x-request-id (%q -> %q); the idempotency key must be stable", ids[0], ids[1])
	}
}

func TestRetryOn429HonorsRetryAfter(t *testing.T) {
	var calls int
	start := time.Now()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := testClient(srv)
	c.backoff = time.Hour // must not be used when Retry-After is present
	if _, _, err := c.do(t.Context(), http.MethodGet, "/x", nil, nil); err != nil {
		t.Fatalf("do: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if elapsed := time.Since(start); elapsed < time.Second {
		t.Errorf("retried after %v, want >= 1s per Retry-After", elapsed)
	}
}

func TestRetryDelay(t *testing.T) {
	c := New(testAPIKey, testUserKey)
	c.backoff = 2 * time.Second
	for _, tc := range []struct {
		header string
		want   time.Duration
	}{
		{"3", 3 * time.Second},
		{"0", 0},
		{" 5 ", 5 * time.Second},
		{"", 2 * time.Second},
		{"soon", 2 * time.Second},
		{"-1", 2 * time.Second},
	} {
		if got := c.retryDelay(tc.header); got != tc.want {
			t.Errorf("retryDelay(%q) = %v, want %v", tc.header, got, tc.want)
		}
	}
}

func TestSecond429IsAnError(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	_, _, err := testClient(srv).do(t.Context(), http.MethodGet, "/x", nil, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusTooManyRequests {
		t.Fatalf("err = %v, want *APIError with status 429", err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want exactly one retry", calls)
	}
}

func TestRetryWaitRespectsContext(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := testClient(srv)
	c.backoff = time.Hour
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, _, err := c.do(ctx, http.MethodGet, "/x", nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (no retry after context expiry)", calls)
	}
}

func TestErrorNeverContainsKeys(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"errorCode":401,"errorMessage":"invalid credentials"}`))
	}))
	defer srv.Close()

	_, _, err := testClient(srv).do(t.Context(), http.MethodGet, "/api/v1/me", nil, nil)
	if err == nil {
		t.Fatal("want error on HTTP 401")
	}
	msg := err.Error()
	if !strings.Contains(msg, "401") || !strings.Contains(msg, "invalid credentials") {
		t.Errorf("error %q should carry status and API message", msg)
	}
	if strings.Contains(msg, testAPIKey) || strings.Contains(msg, testUserKey) {
		t.Errorf("error %q leaks key material", msg)
	}
}

func TestErrorMessageEnvelopes(t *testing.T) {
	long := strings.Repeat("x", maxErrMessage+50)
	for _, tc := range []struct {
		body, want string
	}{
		{`{"errorCode":500,"errorMessage":"gateway boom"}`, "gateway boom"},
		{`{"code":"E1","message":"bad range","requestId":"r1"}`, "bad range"},
		{`{"type":"about:blank","title":"Bad Request","status":400,"detail":"leverage required"}`, "leverage required"},
		{`{"title":"Conflict","status":409}`, "Conflict"},
		{"plain text failure", "plain text failure"},
		{"", "no error detail in response"},
		{long, long[:maxErrMessage]},
	} {
		if got := errorMessage([]byte(tc.body)); got != tc.want {
			t.Errorf("errorMessage(%.40q) = %q, want %q", tc.body, got, tc.want)
		}
	}
}

func TestAny2xxIsSuccess(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusAccepted, http.StatusNoContent} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		}))
		if _, _, err := testClient(srv).do(t.Context(), http.MethodGet, "/x", nil, nil); err != nil {
			t.Errorf("status %d: unexpected error: %v", status, err)
		}
		srv.Close()
	}
}

func TestRequestEncoding(t *testing.T) {
	type payload struct {
		Action      string `json:"action"`
		Transaction string `json:"transaction"`
	}
	var (
		gotMethod, gotQuery, gotContentType string
		gotBody                             []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotQuery = r.URL.RawQuery
		gotContentType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	q := url.Values{"fields": {"instrumentId,internalSymbolFull"}}
	in := payload{Action: "open", Transaction: "buy"}
	if _, _, err := testClient(srv).do(t.Context(), http.MethodPost, "/x", q, in); err != nil {
		t.Fatalf("do: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	// The API ignores comma-separated list values when the commas arrive
	// percent-encoded, so do() must send them literally, not as %2C.
	if want := "fields=instrumentId,internalSymbolFull"; gotQuery != want {
		t.Errorf("query = %q, want %q", gotQuery, want)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	var out payload
	if err := json.Unmarshal(gotBody, &out); err != nil || out != in {
		t.Errorf("body = %s (decode err %v), want round-trip of %+v", gotBody, err, in)
	}
}

func TestNewRequestID(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id, err := newRequestID()
		if err != nil {
			t.Fatalf("newRequestID: %v", err)
		}
		if !uuidV4.MatchString(id) {
			t.Fatalf("id %q is not a canonical UUID v4", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
}
