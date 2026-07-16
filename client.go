package etoro

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const host = "https://public-api.etoro.com"

// userAgent is sent on every request. The API sits behind a CDN that
// rejects generic tool user agents, so a browser-compatible string is used.
const userAgent = "Mozilla/5.0 (compatible; etoro-go)"

// maxErrMessage caps how much of an error response body is carried into an
// APIError when the body is not a recognized JSON error envelope.
const maxErrMessage = 300

// Client is an eToro Public API client. Create with New. The zero value is
// not usable.
type Client struct {
	apiKey  string
	userKey string
	baseURL string
	demo    bool
	client  *http.Client
	backoff time.Duration // 429 retry wait when the response has no Retry-After
}

// Option configures a Client.
type Option func(*Client)

// Demo targets the demo (paper-trading) account for trading endpoints.
// This is the default.
func Demo() Option { return func(c *Client) { c.demo = true } }

// Real targets the real account for trading endpoints. Market-data,
// balances, identity, and cash endpoints are identical in both modes.
func Real() Option { return func(c *Client) { c.demo = false } }

// WithHTTPClient replaces the default HTTP client (30s timeout).
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.client = h } }

// New returns a Client for public-api.etoro.com authenticated with the
// given API key and user key, sent as the x-api-key and x-user-key headers
// on every request. Trading endpoints target the demo account unless the
// Real option is given.
func New(apiKey, userKey string, opts ...Option) *Client {
	c := &Client{
		apiKey:  apiKey,
		userKey: userKey,
		baseURL: host,
		demo:    true,
		client: &http.Client{
			Timeout: 30 * time.Second,
			// The API never redirects; refusing to follow one keeps the
			// x-api-key/x-user-key headers from being replayed to a
			// different host (Go strips only Authorization-class headers
			// on cross-domain redirects, not custom ones).
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		backoff: 2 * time.Second,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// modePath selects between the demo and real form of a trading route.
// The two forms differ only by a path segment, but the segment's position
// varies per route — and the pnl and close-order info routes carry an
// explicit "real" segment on the real side — so callers spell out both
// full paths rather than deriving one from the other.
func (c *Client) modePath(demoPath, realPath string) string {
	if c.demo {
		return demoPath
	}
	return realPath
}

// APIError is a non-2xx response from the API. Message is taken from the
// API's JSON error envelope when one is present; it never contains request
// headers or key material.
type APIError struct {
	Status  int    // HTTP status code
	Message string // best-effort message from the response body
}

func (e *APIError) Error() string {
	return fmt.Sprintf("etoro: HTTP %d: %s", e.Status, e.Message)
}

// do performs one authenticated API call and returns the response body and
// the x-request-id it sent. It sets the three auth headers on the request,
// JSON-encodes body when non-nil, and treats any 2xx status as success.
// A 429 is retried exactly once, honoring Retry-After when present (default
// 2s) and aborting early if ctx is done. The retry reuses the same
// x-request-id: the id is the operation's idempotency key (echoed back as
// referenceId on order submission), so both attempts must present the same
// one. Non-2xx responses are returned as *APIError.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any) ([]byte, string, error) {
	requestID, err := newRequestID()
	if err != nil {
		return nil, "", err
	}
	var payload []byte
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return nil, requestID, fmt.Errorf("etoro: encode request body: %w", err)
		}
	}
	u := c.baseURL + path
	if len(query) > 0 {
		// The API silently ignores comma-separated list values (fields
		// projections, id lists) when the commas arrive percent-encoded,
		// so keep them literal. Commas are valid query sub-delims per
		// RFC 3986 and never appear in other parameter values here.
		u += "?" + strings.ReplaceAll(query.Encode(), "%2C", ",")
	}
	for attempt := 0; ; attempt++ {
		var rd io.Reader
		if payload != nil {
			rd = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, u, rd)
		if err != nil {
			return nil, requestID, err
		}
		req.Header.Set("x-api-key", c.apiKey)
		req.Header.Set("x-user-key", c.userKey)
		req.Header.Set("x-request-id", requestID)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", userAgent)
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.client.Do(req)
		if err != nil {
			return nil, requestID, err
		}
		respBody, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close() // body fully read; close error is non-actionable
		if readErr != nil {
			return nil, requestID, fmt.Errorf("etoro: read body: %w", readErr)
		}
		if resp.StatusCode == http.StatusTooManyRequests && attempt == 0 {
			select {
			case <-ctx.Done():
				return nil, requestID, ctx.Err()
			case <-time.After(c.retryDelay(resp.Header.Get("Retry-After"))):
			}
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return nil, requestID, &APIError{Status: resp.StatusCode, Message: errorMessage(respBody)}
		}
		return respBody, requestID, nil
	}
}

// maxRetryDelay caps how long a Retry-After header can stall the single
// 429 retry; the server controls the header, so an absurd value must not
// block a caller without a context deadline for hours.
const maxRetryDelay = 30 * time.Second

// retryDelay converts a Retry-After header into a wait duration, falling
// back to the client's default backoff when the header is absent or not a
// whole number of seconds, and capping server-supplied values at
// maxRetryDelay.
func (c *Client) retryDelay(retryAfter string) time.Duration {
	if s, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && s >= 0 {
		d := time.Duration(s) * time.Second
		if d > maxRetryDelay {
			return maxRetryDelay
		}
		return d
	}
	return c.backoff
}

// errorMessage extracts a human-readable message from an API error body.
// The API uses several envelopes depending on the service: gateway errors
// {errorCode, errorMessage}, balances errors {code, message, requestId},
// and RFC 7807 problem details {type, title, status, detail} on v2 trading.
// Unrecognized bodies are passed through trimmed and capped; empty bodies
// (e.g. rate-limit responses) yield a generic marker.
func errorMessage(body []byte) string {
	var envelope struct {
		ErrorMessage string `json:"errorMessage"`
		Message      string `json:"message"`
		Detail       string `json:"detail"`
		Title        string `json:"title"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil {
		for _, m := range []string{envelope.ErrorMessage, envelope.Message, envelope.Detail, envelope.Title} {
			if m != "" {
				return m
			}
		}
	}
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		return "no error detail in response"
	}
	if len(msg) > maxErrMessage {
		msg = msg[:maxErrMessage]
	}
	return msg
}

// newRequestID returns a random UUID v4 in canonical form, used as the
// x-request-id header value.
func newRequestID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("etoro: generate request id: %w", err)
	}
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
