// Package httpx is a small JSON-over-HTTP helper with retries, backoff,
// optional rate limiting and structured API errors. It is shared by the
// Formance and Blnk clients.
package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	neturl "net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Authorizer decorates outgoing requests with credentials.
type Authorizer interface {
	Authorize(ctx context.Context, req *http.Request) error
}

// AuthorizerFunc adapts a function to Authorizer.
type AuthorizerFunc func(ctx context.Context, req *http.Request) error

// Authorize implements Authorizer.
func (f AuthorizerFunc) Authorize(ctx context.Context, req *http.Request) error { return f(ctx, req) }

// APIError is returned for non-2xx responses.
type APIError struct {
	Method     string
	URL        string
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	body := e.Body
	if len(body) > 512 {
		body = body[:512] + "…"
	}
	return fmt.Sprintf("%s %s: HTTP %d: %s", e.Method, e.URL, e.StatusCode, strings.TrimSpace(body))
}

// StatusCode extracts the HTTP status from an error chain, or 0.
func StatusCode(err error) int {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.StatusCode
	}
	return 0
}

// Client performs JSON requests.
type Client struct {
	BaseURL    string
	HTTP       *http.Client
	Auth       Authorizer
	UserAgent  string
	MaxRetries int           // retries after the first attempt; default 5
	MinBackoff time.Duration // default 250ms
	MaxBackoff time.Duration // default 10s
	Limiter    *Limiter      // optional
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 60 * time.Second}
}

// Do sends a request. body (if non-nil) is JSON-encoded; out (if non-nil)
// receives the decoded JSON response. Requests are retried on network
// errors, 429 and 5xx responses. Non-idempotent callers must make their
// payloads idempotent (Blnk references do exactly that).
func (c *Client) Do(ctx context.Context, method, path string, body, out any) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
	}
	maxRetries := c.MaxRetries
	if maxRetries == 0 {
		maxRetries = 5
	}
	if maxRetries < 0 {
		maxRetries = 0
	}
	url := strings.TrimRight(c.BaseURL, "/") + path

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			if err := sleep(ctx, c.backoff(attempt, lastErr)); err != nil {
				return err
			}
		}
		if c.Limiter != nil {
			if err := c.Limiter.Wait(ctx); err != nil {
				return err
			}
		}
		retry, err := c.once(ctx, method, url, payload, out)
		if err == nil {
			return nil
		}
		lastErr = err
		if !retry || ctx.Err() != nil {
			return err
		}
	}
	return fmt.Errorf("giving up after %d attempts: %w", maxRetries+1, lastErr)
}

func (c *Client) once(ctx context.Context, method, url string, payload []byte, out any) (retry bool, err error) {
	var rdr io.Reader
	if payload != nil {
		rdr = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	if c.Auth != nil {
		if err := c.Auth.Authorize(ctx, req); err != nil {
			return false, fmt.Errorf("authorize: %w", err)
		}
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return true, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return true, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		apiErr := &APIError{Method: method, URL: redactURL(url), StatusCode: resp.StatusCode, Body: string(data)}
		retry := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		if ra := resp.Header.Get("Retry-After"); ra != "" && retry {
			if s, perr := strconv.Atoi(ra); perr == nil {
				return true, &retryAfterError{APIError: apiErr, after: time.Duration(s) * time.Second}
			}
		}
		return retry, apiErr
	}
	if out != nil && len(bytes.TrimSpace(data)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(data))
		if err := dec.Decode(out); err != nil {
			return false, fmt.Errorf("decode %s %s response: %w", method, redactURL(url), err)
		}
	}
	return false, nil
}

// redactURL hides a password embedded in the URL's userinfo so that it
// never ends up in errors or logs.
func redactURL(raw string) string {
	u, err := neturl.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	return u.Redacted()
}

type retryAfterError struct {
	*APIError
	after time.Duration
}

func (e *retryAfterError) Unwrap() error { return e.APIError }

func (c *Client) backoff(attempt int, lastErr error) time.Duration {
	var ra *retryAfterError
	if errors.As(lastErr, &ra) && ra.after > 0 {
		return ra.after
	}
	min, max := c.MinBackoff, c.MaxBackoff
	if min <= 0 {
		min = 250 * time.Millisecond
	}
	if max <= 0 {
		max = 10 * time.Second
	}
	d := min << (attempt - 1)
	if d > max || d <= 0 {
		d = max
	}
	// Full jitter in [d/2, d].
	return d/2 + time.Duration(rand.Int63n(int64(d/2)+1))
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Limiter is a simple token-bucket rate limiter.
type Limiter struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
}

// NewLimiter returns a limiter allowing rps requests per second. rps <= 0
// returns nil (no limiting).
func NewLimiter(rps float64) *Limiter {
	if rps <= 0 {
		return nil
	}
	return &Limiter{interval: time.Duration(float64(time.Second) / rps)}
}

// Wait blocks until the next request may proceed.
func (l *Limiter) Wait(ctx context.Context) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	now := time.Now()
	if l.next.Before(now) {
		l.next = now
	}
	wait := l.next.Sub(now)
	l.next = l.next.Add(l.interval)
	l.mu.Unlock()
	if wait <= 0 {
		return nil
	}
	return sleep(ctx, wait)
}
