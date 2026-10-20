package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRetriesOn5xxAnd429(t *testing.T) {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch atomic.AddInt32(&n, 1) {
		case 1:
			w.WriteHeader(http.StatusServiceUnavailable)
		case 2:
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			w.Write([]byte(`{"ok":true}`))
		}
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, MinBackoff: time.Millisecond, MaxBackoff: 2 * time.Millisecond}
	var out struct{ OK bool }
	if err := c.Do(context.Background(), http.MethodGet, "/", nil, &out); err != nil || !out.OK {
		t.Fatalf("err=%v out=%+v", err, out)
	}
	if n != 3 {
		t.Fatalf("attempts=%d", n)
	}
}

func TestNoRetryOn4xx(t *testing.T) {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&n, 1)
		http.Error(w, `{"error":"bad"}`, http.StatusBadRequest)
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL}
	err := c.Do(context.Background(), http.MethodPost, "/", map[string]int{"a": 1}, nil)
	if StatusCode(err) != 400 || n != 1 {
		t.Fatalf("err=%v attempts=%d", err, n)
	}
}

func TestLimiter(t *testing.T) {
	l := NewLimiter(100)
	start := time.Now()
	for i := 0; i < 5; i++ {
		if err := l.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if time.Since(start) < 35*time.Millisecond {
		t.Fatal("limiter did not throttle")
	}
	if NewLimiter(0) != nil {
		t.Fatal("expected nil limiter")
	}
}

func TestRetryAfterDoesNotRetryClientErrors(t *testing.T) {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&n, 1)
		w.Header().Set("Retry-After", "0")
		http.Error(w, `{"error":"reference has already been used"}`, http.StatusBadRequest)
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, MinBackoff: time.Millisecond, MaxBackoff: 2 * time.Millisecond}
	err := c.Do(context.Background(), http.MethodPost, "/", map[string]int{"a": 1}, nil)
	if StatusCode(err) != 400 || n != 1 {
		t.Fatalf("err=%v attempts=%d", err, n)
	}
}

func TestErrorsRedactURLPassword(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusBadRequest)
	}))
	defer srv.Close()
	c := &Client{BaseURL: strings.Replace(srv.URL, "http://", "http://user:s3cret@", 1)}
	err := c.Do(context.Background(), http.MethodGet, "/x", nil, nil)
	if err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("password leaked or no error: %v", err)
	}
}
