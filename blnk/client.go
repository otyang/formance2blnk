// Package blnk is a minimal client for the Blnk Finance ledger API covering
// the calls a migration needs: ledgers, balances and (bulk) transactions.
package blnk

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/otyang/formance2blnk/amount"
	"github.com/otyang/formance2blnk/internal/httpx"
)

// Transaction statuses reported by Blnk.
const (
	StatusQueued   = "QUEUED"
	StatusApplied  = "APPLIED"
	StatusInflight = "INFLIGHT"
	StatusVoid     = "VOID"
	StatusRejected = "REJECTED"
)

// MaxAtomicBulk is the largest batch Blnk accepts in atomic bulk mode.
const MaxAtomicBulk = 50

// Options configures a Client.
type Options struct {
	BaseURL           string // e.g. http://localhost:5001
	APIKey            string // sent as X-Blnk-Key; empty for unsecured instances
	HTTPClient        *http.Client
	RequestsPerSecond float64
	MaxRetries        int
	UserAgent         string
}

// Client talks to a Blnk server.
type Client struct {
	http *httpx.Client
}

// New returns a Client.
func New(opts Options) (*Client, error) {
	if opts.BaseURL == "" {
		return nil, fmt.Errorf("blnk: BaseURL is required")
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
	if opts.APIKey != "" {
		key := opts.APIKey
		hc.Auth = httpx.AuthorizerFunc(func(_ context.Context, r *http.Request) error {
			r.Header.Set("X-Blnk-Key", key)
			return nil
		})
	}
	return &Client{http: hc}, nil
}

// Ledger is a Blnk ledger.
type Ledger struct {
	LedgerID string         `json:"ledger_id"`
	Name     string         `json:"name"`
	MetaData map[string]any `json:"meta_data,omitempty"`
}

// CreateLedger creates a ledger.
func (c *Client) CreateLedger(ctx context.Context, name string, meta map[string]any) (Ledger, error) {
	var out Ledger
	err := c.http.Do(ctx, http.MethodPost, "/ledgers", map[string]any{"name": name, "meta_data": meta}, &out)
	if err == nil && out.LedgerID == "" {
		err = errors.New("blnk: create ledger returned no ledger_id")
	}
	return out, err
}

// BalanceRequest creates a balance.
type BalanceRequest struct {
	LedgerID   string         `json:"ledger_id"`
	Currency   string         `json:"currency"`
	IdentityID string         `json:"identity_id,omitempty"`
	MetaData   map[string]any `json:"meta_data,omitempty"`
}

// Balance is a Blnk balance. Amounts are in minor units.
type Balance struct {
	BalanceID       string         `json:"balance_id"`
	LedgerID        string         `json:"ledger_id"`
	Indicator       string         `json:"indicator,omitempty"`
	Currency        string         `json:"currency"`
	Balance         amount.Amount  `json:"balance"`
	CreditBalance   amount.Amount  `json:"credit_balance"`
	DebitBalance    amount.Amount  `json:"debit_balance"`
	InflightBalance amount.Amount  `json:"inflight_balance"`
	MetaData        map[string]any `json:"meta_data,omitempty"`
}

// CreateBalance creates a balance.
func (c *Client) CreateBalance(ctx context.Context, req BalanceRequest) (Balance, error) {
	var out Balance
	err := c.http.Do(ctx, http.MethodPost, "/balances", req, &out)
	if err == nil && out.BalanceID == "" {
		err = errors.New("blnk: create balance returned no balance_id")
	}
	return out, err
}

// GetBalance fetches a balance by ID.
func (c *Client) GetBalance(ctx context.Context, id string) (Balance, error) {
	var out Balance
	err := c.http.Do(ctx, http.MethodGet, "/balances/"+url.PathEscape(id), nil, &out)
	return out, err
}

// GetBalanceByIndicator fetches an internal (indicator) balance such as
// "@World" for a currency.
func (c *Client) GetBalanceByIndicator(ctx context.Context, indicator, currency string) (Balance, error) {
	var out Balance
	p := "/balances/indicator/" + url.PathEscape(indicator) + "/currency/" + url.PathEscape(currency)
	err := c.http.Do(ctx, http.MethodGet, p, nil, &out)
	return out, err
}

// TransactionRequest records a transaction. Source and Destination are
// balance IDs or "@Indicator" names.
type TransactionRequest struct {
	PreciseAmount  amount.Amount  `json:"precise_amount"`
	Precision      amount.Amount  `json:"precision"`
	Reference      string         `json:"reference"`
	Currency       string         `json:"currency"`
	Source         string         `json:"source"`
	Destination    string         `json:"destination"`
	Description    string         `json:"description,omitempty"`
	AllowOverdraft bool           `json:"allow_overdraft"`
	SkipQueue      bool           `json:"skip_queue"`
	EffectiveDate  *time.Time     `json:"effective_date,omitempty"`
	MetaData       map[string]any `json:"meta_data,omitempty"`
}

// Transaction is the server's view of a recorded transaction.
type Transaction struct {
	TransactionID string `json:"transaction_id"`
	Reference     string `json:"reference"`
	Status        string `json:"status"`
}

// CreateTransaction records a single transaction. A REJECTED status is
// returned as an error.
func (c *Client) CreateTransaction(ctx context.Context, req TransactionRequest) (Transaction, error) {
	var out Transaction
	if err := c.http.Do(ctx, http.MethodPost, "/transactions", req, &out); err != nil {
		return out, err
	}
	if strings.EqualFold(out.Status, StatusRejected) {
		return out, &RejectedError{Reference: req.Reference}
	}
	return out, nil
}

// BulkRequest records several transactions in one call.
type BulkRequest struct {
	Transactions []TransactionRequest `json:"transactions"`
	Atomic       bool                 `json:"atomic"`
	RunAsync     bool                 `json:"run_async"`
	SkipQueue    bool                 `json:"skip_queue"`
}

// BulkResult is the response of a bulk call.
type BulkResult struct {
	BatchID string `json:"batch_id"`
	Status  string `json:"status"`
	Error   string `json:"error,omitempty"`
}

// CreateBulk records a batch. In synchronous atomic mode either every item
// is applied or none is.
func (c *Client) CreateBulk(ctx context.Context, req BulkRequest) (BulkResult, error) {
	if req.Atomic && len(req.Transactions) > MaxAtomicBulk {
		return BulkResult{}, fmt.Errorf("blnk: atomic bulk is limited to %d transactions, got %d", MaxAtomicBulk, len(req.Transactions))
	}
	var out BulkResult
	if err := c.http.Do(ctx, http.MethodPost, "/transactions/bulk", req, &out); err != nil {
		return out, err
	}
	if out.Error != "" || strings.EqualFold(out.Status, "failed") || strings.EqualFold(out.Status, StatusRejected) {
		return out, fmt.Errorf("blnk: bulk %s failed: %s", out.BatchID, out.Error)
	}
	return out, nil
}

// RejectedError means Blnk accepted the request but rejected the
// transaction (for example for insufficient funds).
type RejectedError struct{ Reference string }

func (e *RejectedError) Error() string {
	return fmt.Sprintf("blnk: transaction %s was rejected", e.Reference)
}

// IsDuplicateReference reports whether err means a transaction with the
// same reference already exists — i.e. it was migrated by an earlier run.
func IsDuplicateReference(err error) bool {
	if err == nil {
		return false
	}
	code := httpx.StatusCode(err)
	if code != http.StatusBadRequest && code != http.StatusConflict && code != http.StatusUnprocessableEntity {
		return false
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "reference") {
		return false
	}
	for _, kw := range []string{"already", "duplicate", "exists", "used"} {
		if strings.Contains(msg, kw) {
			return true
		}
	}
	return false
}

// IsNotFound reports whether err is an HTTP 404.
func IsNotFound(err error) bool { return httpx.StatusCode(err) == http.StatusNotFound }
