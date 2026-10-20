// Package formance is a minimal, read-only client for the Formance Ledger v2
// REST API. It covers exactly what a migration needs: listing ledgers,
// streaming transactions in ID order, reading volumes and account metadata.
package formance

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/otyang/formance2blnk/internal/httpx"
)

// Options configures a Client.
type Options struct {
	// BaseURL is the Ledger API root, without the "/v2" segment. Examples:
	//   http://localhost:3068                              (self-hosted ledger)
	//   https://ORG.ENV.formance.cloud/api/ledger          (Formance Cloud)
	BaseURL string

	// Exactly one of the following may be set. With none, requests are
	// unauthenticated.
	BearerToken  string
	ClientID     string // OAuth2 client credentials
	ClientSecret string
	TokenURL     string // defaults to <origin>/api/auth/oauth/token
	Scopes       []string

	// PageSize for list calls. Default 100.
	PageSize int

	// AscendingParams are query parameters that make the transactions list
	// return oldest-first. Default "reverse=true". If the server ignores
	// them, the client detects descending order and buffers instead.
	AscendingParams string

	HTTPClient        *http.Client
	RequestsPerSecond float64
	MaxRetries        int
	UserAgent         string
}

// Client reads from a Formance ledger.
type Client struct {
	opts Options
	http *httpx.Client
}

// New returns a Client.
func New(opts Options) (*Client, error) {
	if opts.BaseURL == "" {
		return nil, fmt.Errorf("formance: BaseURL is required")
	}
	if opts.PageSize <= 0 {
		opts.PageSize = 100
	}
	if opts.AscendingParams == "" {
		opts.AscendingParams = "reverse=true"
	}
	if opts.UserAgent == "" {
		opts.UserAgent = "formance2blnk"
	}
	hc := &httpx.Client{
		BaseURL:    opts.BaseURL,
		HTTP:       opts.HTTPClient,
		UserAgent:  opts.UserAgent,
		MaxRetries: opts.MaxRetries,
		Limiter:    httpx.NewLimiter(opts.RequestsPerSecond),
	}
	switch {
	case opts.BearerToken != "":
		tok := opts.BearerToken
		hc.Auth = httpx.AuthorizerFunc(func(_ context.Context, r *http.Request) error {
			r.Header.Set("Authorization", "Bearer "+tok)
			return nil
		})
	case opts.ClientID != "":
		tokenURL := opts.TokenURL
		if tokenURL == "" {
			u, err := url.Parse(opts.BaseURL)
			if err != nil {
				return nil, fmt.Errorf("formance: parse BaseURL: %w", err)
			}
			tokenURL = u.Scheme + "://" + u.Host + "/api/auth/oauth/token"
		}
		hc.Auth = &oauthSource{
			tokenURL: tokenURL, id: opts.ClientID, secret: opts.ClientSecret,
			scopes: opts.Scopes, http: opts.HTTPClient,
		}
	}
	return &Client{opts: opts, http: hc}, nil
}

// ListLedgers returns all ledgers on the stack.
func (c *Client) ListLedgers(ctx context.Context) ([]Ledger, error) {
	var out []Ledger
	err := paginate(ctx, c, "/v2", url.Values{}, func(page []Ledger) (bool, error) {
		out = append(out, page...)
		return true, nil
	})
	return out, err
}

// GetAccount returns an account and its metadata.
func (c *Client) GetAccount(ctx context.Context, ledger, address string) (Account, error) {
	var res single[Account]
	p := fmt.Sprintf("/v2/%s/accounts/%s", url.PathEscape(ledger), url.PathEscape(address))
	if err := c.http.Do(ctx, http.MethodGet, p, nil, &res); err != nil {
		return Account{}, err
	}
	if res.Data.Address == "" {
		res.Data.Address = address
	}
	return res.Data, nil
}

// GetTransaction returns one transaction by ID.
func (c *Client) GetTransaction(ctx context.Context, ledger string, id uint64) (Transaction, error) {
	var res single[Transaction]
	p := fmt.Sprintf("/v2/%s/transactions/%d", url.PathEscape(ledger), id)
	err := c.http.Do(ctx, http.MethodGet, p, nil, &res)
	return res.Data, err
}

// VolumesOptions filters a volumes listing.
type VolumesOptions struct {
	// EndTime limits volumes to transactions effective strictly before this
	// time (point-in-time balances). Zero means "now".
	EndTime time.Time
	// InsertionDate makes EndTime apply to insertion date rather than the
	// effective timestamp.
	InsertionDate bool
}

// Volumes streams volumes with balances for every (account, asset) pair.
func (c *Client) Volumes(ctx context.Context, ledger string, opts VolumesOptions, fn func(Volume) error) error {
	q := url.Values{}
	if !opts.EndTime.IsZero() {
		q.Set("endTime", opts.EndTime.UTC().Format(time.RFC3339Nano))
	}
	if opts.InsertionDate {
		q.Set("insertionDate", "true")
	}
	return paginate(ctx, c, "/v2/"+url.PathEscape(ledger)+"/volumes", q, func(page []Volume) (bool, error) {
		for _, v := range page {
			if err := fn(v); err != nil {
				return false, err
			}
		}
		return true, nil
	})
}

// Transactions calls fn for every transaction with ID > afterID, in strictly
// ascending ID order. Return ErrStop from fn to end early without error.
func (c *Client) Transactions(ctx context.Context, ledger string, afterID *uint64, fn func(Transaction) error) error {
	q, err := url.ParseQuery(c.opts.AscendingParams)
	if err != nil {
		return fmt.Errorf("formance: invalid AscendingParams: %w", err)
	}
	path := "/v2/" + url.PathEscape(ledger) + "/transactions"

	after := func(id uint64) bool { return afterID == nil || id > *afterID }

	var (
		order    int // 0 unknown, +1 ascending, -1 descending
		pending  []Transaction
		buffered []Transaction
		lastID   uint64
		haveLast bool
	)
	emit := func(t Transaction) error {
		if !after(t.ID) {
			return nil
		}
		if haveLast && t.ID <= lastID {
			return fmt.Errorf("formance: transactions out of order (%d after %d)", t.ID, lastID)
		}
		lastID, haveLast = t.ID, true
		return fn(t)
	}

	err = paginate(ctx, c, path, q, func(page []Transaction) (bool, error) {
		for _, t := range page {
			if order == 0 {
				if len(pending) > 0 {
					if t.ID > pending[len(pending)-1].ID {
						order = 1
					} else {
						order = -1
					}
				}
				pending = append(pending, t)
				if order == 1 {
					for _, p := range pending {
						if err := emit(p); err != nil {
							return false, err
						}
					}
					pending = nil
				} else if order == -1 {
					buffered = append(buffered, pending...)
					pending = nil
				}
				continue
			}
			if order == 1 {
				if err := emit(t); err != nil {
					return false, err
				}
				continue
			}
			// Descending: everything older than the checkpoint is already
			// migrated, so we can stop paging.
			if !after(t.ID) {
				return false, nil
			}
			buffered = append(buffered, t)
		}
		return true, nil
	})
	if err != nil {
		if err == ErrStop {
			return nil
		}
		return err
	}
	buffered = append(buffered, pending...)
	sort.Slice(buffered, func(i, j int) bool { return buffered[i].ID < buffered[j].ID })
	for _, t := range buffered {
		if err := emit(t); err != nil {
			if err == ErrStop {
				return nil
			}
			return err
		}
	}
	return nil
}

// ErrStop can be returned from iteration callbacks to stop without error.
var ErrStop = fmt.Errorf("formance: stop iteration")

func paginate[T any](ctx context.Context, c *Client, path string, q url.Values, fn func([]T) (bool, error)) error {
	first := url.Values{}
	for k, v := range q {
		first[k] = v
	}
	first.Set("pageSize", strconv.Itoa(c.opts.PageSize))
	next := ""
	for {
		var p string
		if next == "" {
			p = path + "?" + first.Encode()
		} else {
			// When a cursor is given, Formance rejects other parameters.
			p = path + "?cursor=" + url.QueryEscape(next)
		}
		var page cursor[T]
		if err := c.http.Do(ctx, http.MethodGet, p, nil, &page); err != nil {
			return err
		}
		cont, err := fn(page.Cursor.Data)
		if err != nil {
			if err == ErrStop {
				return nil
			}
			return err
		}
		if !cont || !page.Cursor.HasMore {
			return nil
		}
		// Stopping here would silently truncate the listing (and let the
		// caller checkpoint past data it never saw); looping on the same
		// cursor would never end.
		if page.Cursor.Next == "" {
			return fmt.Errorf("formance: %s: page has more results but no next cursor", path)
		}
		if page.Cursor.Next == next {
			return fmt.Errorf("formance: %s: server returned the same cursor twice", path)
		}
		next = page.Cursor.Next
	}
}

// oauthSource implements OAuth2 client-credentials with token caching.
type oauthSource struct {
	tokenURL, id, secret string
	scopes               []string
	http                 *http.Client

	mu      sync.Mutex
	token   string
	expires time.Time
}

func (o *oauthSource) Authorize(ctx context.Context, r *http.Request) error {
	tok, err := o.get(ctx)
	if err != nil {
		return err
	}
	r.Header.Set("Authorization", "Bearer "+tok)
	return nil
}

func (o *oauthSource) get(ctx context.Context) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.token != "" && time.Until(o.expires) > 30*time.Second {
		return o.token, nil
	}
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {o.id}, "client_secret": {o.secret}}
	if len(o.scopes) > 0 {
		form.Set("scope", strings.Join(o.scopes, " "))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	hc := o.http
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("oauth token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("oauth token: HTTP %d", resp.StatusCode)
	}
	var body struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("oauth token: %w", err)
	}
	if body.AccessToken == "" {
		return "", fmt.Errorf("oauth token: empty access_token")
	}
	if body.ExpiresIn <= 0 {
		body.ExpiresIn = 300
	}
	o.token = body.AccessToken
	o.expires = time.Now().Add(time.Duration(body.ExpiresIn) * time.Second)
	return o.token, nil
}
