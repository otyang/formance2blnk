package fakes

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	"github.com/otyang/formance2blnk/blnk"
)

// Blnk is a fake Blnk server with real double-entry semantics, duplicate
// reference detection and atomic bulk rollback.
type Blnk struct {
	mu         sync.Mutex
	server     *httptest.Server
	seq        int
	ledgers    map[string]blnk.Ledger
	balances   map[string]*blnk.Balance
	indicators map[string]string // "@X|USD" -> balance id
	refs       map[string]blnk.TransactionRequest

	// LoseResponseAt, when > 0, makes the Nth applied transaction succeed
	// but return HTTP 502, simulating a lost response.
	LoseResponseAt int
	applied        int
	// Calls counts transaction-writing HTTP calls.
	Calls int
}

// NewBlnk starts a fake server. Call Close when done.
func NewBlnk() *Blnk {
	b := &Blnk{
		ledgers:    map[string]blnk.Ledger{},
		balances:   map[string]*blnk.Balance{},
		indicators: map[string]string{},
		refs:       map[string]blnk.TransactionRequest{},
	}
	b.server = httptest.NewServer(http.HandlerFunc(b.serve))
	return b
}

// URL is the base URL to give to blnk.Options.BaseURL.
func (b *Blnk) URL() string { return b.server.URL }

// Close stops the server.
func (b *Blnk) Close() { b.server.Close() }

// TransactionCount returns the number of recorded transactions.
func (b *Blnk) TransactionCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.refs)
}

// LedgerCount returns the number of ledgers created.
func (b *Blnk) LedgerCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.ledgers)
}

// Transaction returns a recorded transaction by reference.
func (b *Blnk) Transaction(ref string) (blnk.TransactionRequest, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	t, ok := b.refs[ref]
	return t, ok
}

func (b *Blnk) id(prefix string) string {
	b.seq++
	return fmt.Sprintf("%s_%06d", prefix, b.seq)
}

type apiErr struct {
	code int
	msg  string
}

func (b *Blnk) serve(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	path := strings.Trim(r.URL.Path, "/")
	parts := strings.Split(path, "/")
	switch {
	case r.Method == http.MethodPost && path == "ledgers":
		var req struct {
			Name     string         `json:"name"`
			MetaData map[string]any `json:"meta_data"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.Name == "" {
			fail(w, apiErr{400, "name is required"})
			return
		}
		l := blnk.Ledger{LedgerID: b.id("ldg"), Name: req.Name, MetaData: req.MetaData}
		b.ledgers[l.LedgerID] = l
		writeJSON(w, l)
	case r.Method == http.MethodPost && path == "balances":
		var req blnk.BalanceRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			fail(w, apiErr{400, "bad json"})
			return
		}
		if _, ok := b.ledgers[req.LedgerID]; !ok {
			fail(w, apiErr{400, "ledger not found"})
			return
		}
		bal := &blnk.Balance{BalanceID: b.id("bln"), LedgerID: req.LedgerID, Currency: req.Currency, MetaData: req.MetaData}
		b.balances[bal.BalanceID] = bal
		writeJSON(w, bal)
	case r.Method == http.MethodGet && len(parts) == 2 && parts[0] == "balances":
		bal, ok := b.balances[parts[1]]
		if !ok {
			fail(w, apiErr{404, "balance not found"})
			return
		}
		writeJSON(w, bal)
	case r.Method == http.MethodGet && len(parts) == 5 && parts[0] == "balances" && parts[1] == "indicator":
		id, ok := b.indicators[parts[2]+"|"+parts[4]]
		if !ok {
			fail(w, apiErr{404, "balance not found"})
			return
		}
		writeJSON(w, b.balances[id])
	case r.Method == http.MethodPost && path == "transactions":
		b.Calls++
		var req blnk.TransactionRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			fail(w, apiErr{400, "bad json"})
			return
		}
		if e := b.apply(req); e != nil {
			fail(w, *e)
			return
		}
		if b.lose() {
			fail(w, apiErr{502, "bad gateway"})
			return
		}
		writeJSON(w, blnk.Transaction{TransactionID: b.id("txn"), Reference: req.Reference, Status: blnk.StatusApplied})
	case r.Method == http.MethodPost && path == "transactions/bulk":
		b.Calls++
		var req blnk.BulkRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			fail(w, apiErr{400, "bad json"})
			return
		}
		if req.Atomic && len(req.Transactions) > blnk.MaxAtomicBulk {
			fail(w, apiErr{400, "too many transactions for atomic batch"})
			return
		}
		snap := b.clone()
		for _, t := range req.Transactions {
			if e := b.apply(t); e != nil {
				if req.Atomic {
					b.restore(snap)
				}
				fail(w, apiErr{e.code, "transaction failed: " + e.msg})
				return
			}
		}
		if b.lose() {
			fail(w, apiErr{502, "bad gateway"})
			return
		}
		writeJSON(w, blnk.BulkResult{BatchID: b.id("bulk"), Status: "applied"})
	default:
		fail(w, apiErr{404, "not found"})
	}
}

func (b *Blnk) lose() bool {
	b.applied++
	return b.LoseResponseAt > 0 && b.applied == b.LoseResponseAt
}

func (b *Blnk) resolve(id, currency string) (*blnk.Balance, *apiErr) {
	if strings.HasPrefix(id, "@") {
		k := id + "|" + currency
		if bid, ok := b.indicators[k]; ok {
			return b.balances[bid], nil
		}
		bal := &blnk.Balance{BalanceID: b.id("bln"), LedgerID: "general_ledger_id", Indicator: id, Currency: currency}
		b.balances[bal.BalanceID] = bal
		b.indicators[k] = bal.BalanceID
		return bal, nil
	}
	bal, ok := b.balances[id]
	if !ok {
		return nil, &apiErr{400, "balance " + id + " not found"}
	}
	if bal.Currency != currency {
		return nil, &apiErr{400, "currency mismatch on balance " + id}
	}
	return bal, nil
}

func (b *Blnk) apply(t blnk.TransactionRequest) *apiErr {
	if t.Reference == "" {
		return &apiErr{400, "reference is required"}
	}
	if _, dup := b.refs[t.Reference]; dup {
		return &apiErr{400, "reference " + t.Reference + " has already been used"}
	}
	if t.PreciseAmount.Sign() <= 0 {
		return &apiErr{400, "amount must be positive"}
	}
	if t.Source == t.Destination {
		return &apiErr{400, "source and destination cannot be the same"}
	}
	src, e := b.resolve(t.Source, t.Currency)
	if e != nil {
		return e
	}
	dst, e := b.resolve(t.Destination, t.Currency)
	if e != nil {
		return e
	}
	if !t.AllowOverdraft && src.Indicator == "" && src.Balance.Cmp(t.PreciseAmount) < 0 {
		return &apiErr{400, "insufficient funds in " + src.BalanceID}
	}
	src.DebitBalance = src.DebitBalance.Add(t.PreciseAmount)
	src.Balance = src.Balance.Sub(t.PreciseAmount)
	dst.CreditBalance = dst.CreditBalance.Add(t.PreciseAmount)
	dst.Balance = dst.Balance.Add(t.PreciseAmount)
	b.refs[t.Reference] = t
	return nil
}

type blnkSnap struct {
	balances   map[string]blnk.Balance
	indicators map[string]string
	refs       map[string]blnk.TransactionRequest
}

func (b *Blnk) clone() blnkSnap {
	s := blnkSnap{balances: map[string]blnk.Balance{}, indicators: map[string]string{}, refs: map[string]blnk.TransactionRequest{}}
	for k, v := range b.balances {
		s.balances[k] = *v
	}
	for k, v := range b.indicators {
		s.indicators[k] = v
	}
	for k, v := range b.refs {
		s.refs[k] = v
	}
	return s
}

func (b *Blnk) restore(s blnkSnap) {
	b.balances = map[string]*blnk.Balance{}
	for k, v := range s.balances {
		v := v
		b.balances[k] = &v
	}
	b.indicators, b.refs = s.indicators, s.refs
}

func fail(w http.ResponseWriter, e apiErr) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": e.msg})
}
