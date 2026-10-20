// Package fakes provides in-memory HTTP fakes of the Formance Ledger v2 and
// Blnk APIs, used by the test suite and the examples. They implement only
// the subset of behaviour the migrator relies on.
package fakes

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/otyang/formance2blnk/amount"
	"github.com/otyang/formance2blnk/formance"
)

// Formance is a fake Formance ledger server.
type Formance struct {
	mu      sync.Mutex
	ledgers map[string]*fLedger
	names   []string
	server  *httptest.Server
	// IgnoreReverse makes the server always return newest-first, to test the
	// client's descending-order fallback.
	IgnoreReverse bool
	// Requests counts HTTP requests served.
	Requests int
}

type fLedger struct {
	txs      []formance.Transaction
	accounts map[string]formance.Metadata
}

// NewFormance starts a fake server. Call Close when done.
func NewFormance() *Formance {
	f := &Formance{ledgers: map[string]*fLedger{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	return f
}

// URL is the base URL to give to formance.Options.BaseURL.
func (f *Formance) URL() string { return f.server.URL }

// Close stops the server.
func (f *Formance) Close() { f.server.Close() }

func (f *Formance) ledger(name string) *fLedger {
	l := f.ledgers[name]
	if l == nil {
		l = &fLedger{accounts: map[string]formance.Metadata{}}
		f.ledgers[name] = l
		f.names = append(f.names, name)
	}
	return l
}

// Post records a transaction and returns its ID.
func (f *Formance) Post(ledger string, ts time.Time, postings []formance.Posting, meta map[string]string) uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	l := f.ledger(ledger)
	id := uint64(len(l.txs))
	l.txs = append(l.txs, formance.Transaction{ID: id, Timestamp: ts.UTC(), Postings: postings, Metadata: meta})
	for _, p := range postings {
		l.touch(p.Source)
		l.touch(p.Destination)
	}
	return id
}

// Revert reverts a transaction at time ts and returns the new ID.
func (f *Formance) Revert(ledger string, id uint64, ts time.Time) uint64 {
	f.mu.Lock()
	orig := f.ledger(ledger).txs[id]
	f.ledger(ledger).txs[id].Reverted = true
	f.mu.Unlock()
	var rev []formance.Posting
	for i := len(orig.Postings) - 1; i >= 0; i-- {
		p := orig.Postings[i]
		rev = append(rev, formance.Posting{Source: p.Destination, Destination: p.Source, Amount: p.Amount, Asset: p.Asset})
	}
	return f.Post(ledger, ts, rev, map[string]string{formance.MetadataRevertsKey: strconv.FormatUint(id, 10)})
}

// SetAccountMetadata sets account metadata.
func (f *Formance) SetAccountMetadata(ledger, address string, meta map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ledger(ledger).accounts[address] = meta
}

func (l *fLedger) touch(addr string) {
	if _, ok := l.accounts[addr]; !ok {
		l.accounts[addr] = formance.Metadata{}
	}
}

// Volumes computes volumes for effective timestamps strictly before end
// (zero end = all).
func (f *Formance) Volumes(ledger string, end time.Time) []formance.Volume {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.volumes(f.ledger(ledger), end)
}

func (f *Formance) volumes(l *fLedger, end time.Time) []formance.Volume {
	type key struct{ acc, asset string }
	vols := map[key]*formance.Volume{}
	get := func(acc, asset string) *formance.Volume {
		k := key{acc, asset}
		if vols[k] == nil {
			vols[k] = &formance.Volume{Account: acc, Asset: asset}
		}
		return vols[k]
	}
	for _, t := range l.txs {
		if !end.IsZero() && !t.Timestamp.Before(end) {
			continue
		}
		for _, p := range t.Postings {
			s := get(p.Source, p.Asset)
			s.Output = s.Output.Add(p.Amount)
			d := get(p.Destination, p.Asset)
			d.Input = d.Input.Add(p.Amount)
		}
	}
	out := make([]formance.Volume, 0, len(vols))
	for _, v := range vols {
		v.Balance = v.Input.Sub(v.Output)
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Account != out[j].Account {
			return out[i].Account < out[j].Account
		}
		return out[i].Asset < out[j].Asset
	})
	return out
}

type fCursor struct {
	Offset  int    `json:"o"`
	Size    int    `json:"s"`
	Reverse bool   `json:"r"`
	End     string `json:"e,omitempty"`
}

func (f *Formance) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Requests++
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) == 0 || parts[0] != "v2" || r.Method != http.MethodGet {
		http.Error(w, `{"errorCode":"NOT_FOUND"}`, http.StatusNotFound)
		return
	}
	q := r.URL.Query()
	cur := fCursor{Size: 15}
	if c := q.Get("cursor"); c != "" {
		if len(q) > 1 {
			http.Error(w, `{"errorCode":"VALIDATION","errorMessage":"no other query params with cursor"}`, 400)
			return
		}
		b, _ := base64.RawURLEncoding.DecodeString(c)
		_ = json.Unmarshal(b, &cur)
	} else {
		if s, err := strconv.Atoi(q.Get("pageSize")); err == nil && s > 0 {
			cur.Size = s
		}
		cur.Reverse = q.Get("reverse") == "true" && !f.IgnoreReverse
		cur.End = q.Get("endTime")
	}

	switch {
	case len(parts) == 1:
		var data []formance.Ledger
		for _, n := range f.names {
			data = append(data, formance.Ledger{Name: n})
		}
		writePage(w, data, cur)
	case len(parts) == 3 && parts[2] == "transactions":
		l, ok := f.ledgers[parts[1]]
		if !ok {
			http.Error(w, `{"errorCode":"NOT_FOUND"}`, 404)
			return
		}
		txs := append([]formance.Transaction(nil), l.txs...)
		if !cur.Reverse { // default: newest first
			sort.Slice(txs, func(i, j int) bool { return txs[i].ID > txs[j].ID })
		}
		writePage(w, txs, cur)
	case len(parts) == 4 && parts[2] == "transactions":
		l, ok := f.ledgers[parts[1]]
		id, err := strconv.Atoi(parts[3])
		if !ok || err != nil || id < 0 || id >= len(l.txs) {
			http.Error(w, `{"errorCode":"NOT_FOUND"}`, 404)
			return
		}
		writeJSON(w, map[string]any{"data": l.txs[id]})
	case len(parts) == 3 && parts[2] == "volumes":
		l, ok := f.ledgers[parts[1]]
		if !ok {
			http.Error(w, `{"errorCode":"NOT_FOUND"}`, 404)
			return
		}
		var end time.Time
		if cur.End != "" {
			end, _ = time.Parse(time.RFC3339Nano, cur.End)
		}
		writePage(w, f.volumes(l, end), cur)
	case len(parts) == 4 && parts[2] == "accounts":
		l, ok := f.ledgers[parts[1]]
		if !ok {
			http.Error(w, `{"errorCode":"NOT_FOUND"}`, 404)
			return
		}
		writeJSON(w, map[string]any{"data": formance.Account{Address: parts[3], Metadata: l.accounts[parts[3]]}})
	default:
		http.Error(w, `{"errorCode":"NOT_FOUND"}`, 404)
	}
}

func writePage[T any](w http.ResponseWriter, all []T, cur fCursor) {
	end := cur.Offset + cur.Size
	if end > len(all) {
		end = len(all)
	}
	start := cur.Offset
	if start > len(all) {
		start = len(all)
	}
	page := struct {
		PageSize int    `json:"pageSize"`
		HasMore  bool   `json:"hasMore"`
		Next     string `json:"next,omitempty"`
		Data     []T    `json:"data"`
	}{PageSize: cur.Size, HasMore: end < len(all), Data: all[start:end]}
	if page.HasMore {
		nc := cur
		nc.Offset = end
		b, _ := json.Marshal(nc)
		page.Next = base64.RawURLEncoding.EncodeToString(b)
	}
	writeJSON(w, map[string]any{"cursor": page})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// Amt is a test helper turning an int64 into an amount.
func Amt(v int64) amount.Amount { return amount.New(v) }
