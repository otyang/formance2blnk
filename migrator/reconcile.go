package migrator

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/otyang/formance2blnk/amount"
	"github.com/otyang/formance2blnk/blnk"
	"github.com/otyang/formance2blnk/formance"
	"github.com/otyang/formance2blnk/mapping"
)

// ReconcileOptions configures Reconcile.
type ReconcileOptions struct {
	// CheckVolumes also compares Blnk credit/debit totals against Formance
	// input/output volumes. Only meaningful after a full Replay with the
	// default revert policy.
	CheckVolumes bool
	// SkipIndicators ignores internal (@Indicator) balances.
	SkipIndicators bool
	// Workers is the number of concurrent Blnk balance lookups. Default 8.
	Workers int
}

// Mismatch is one difference between Formance and Blnk.
type Mismatch struct {
	Target   string `json:"target"`   // balance ID or @Indicator/CURRENCY
	Accounts string `json:"accounts"` // Formance ledger:address (asset)
	Field    string `json:"field"`    // balance, credit_balance, debit_balance, missing
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
	Detail   string `json:"detail,omitempty"`
}

// Report is the result of a reconciliation.
type Report struct {
	GeneratedAt time.Time  `json:"generated_at"`
	Checked     int        `json:"checked"`
	Matched     int        `json:"matched"`
	Skipped     int        `json:"skipped"`
	Mismatches  []Mismatch `json:"mismatches"`
}

// OK reports whether the ledgers agree.
func (r Report) OK() bool { return len(r.Mismatches) == 0 }

type expectation struct {
	target    string
	accounts  []string
	balanceID string
	indicator string
	currency  string
	input     amount.Amount
	output    amount.Amount
	balance   amount.Amount
}

// Reconcile compares current Formance volumes with Blnk balances.
func (m *Migrator) Reconcile(ctx context.Context, opts ReconcileOptions) (Report, error) {
	if opts.Workers <= 0 {
		opts.Workers = 8
	}
	rep := Report{GeneratedAt: time.Now().UTC()}
	exps := map[string]*expectation{}
	var order []string

	for _, ledger := range m.opts.Ledgers {
		err := m.src.Volumes(ctx, ledger, formance.VolumesOptions{}, func(v formance.Volume) error {
			label := fmt.Sprintf("%s:%s (%s)", ledger, v.Account, v.Asset)
			t, err := m.opts.Mapper.Account(ledger, v.Account)
			if err != nil {
				return err
			}
			asset, err := m.opts.Mapper.Asset(v.Asset)
			if err != nil {
				return err
			}
			var key string
			var e *expectation
			switch t.Kind {
			case mapping.KindSkip:
				rep.Skipped++
				return nil
			case mapping.KindIndicator:
				if opts.SkipIndicators {
					rep.Skipped++
					return nil
				}
				key = t.Indicator + "/" + asset.Currency
				if e = exps[key]; e == nil {
					e = &expectation{target: key, indicator: t.Indicator, currency: asset.Currency}
				}
			default:
				rec, ok := m.opts.Store.Balance(stateKey(ledger, v.Account, v.Asset))
				if !ok {
					if v.Balance.IsZero() && (!opts.CheckVolumes || (v.Input.IsZero() && v.Output.IsZero())) {
						rep.Skipped++
						return nil
					}
					rep.Checked++
					rep.Mismatches = append(rep.Mismatches, Mismatch{
						Target: "-", Accounts: label, Field: "missing",
						Expected: v.Balance.String(), Actual: "-", Detail: "no Blnk balance recorded in state",
					})
					return nil
				}
				key = rec.BalanceID
				if e = exps[key]; e == nil {
					e = &expectation{target: key, balanceID: rec.BalanceID, currency: asset.Currency}
				}
			}
			if _, seen := exps[key]; !seen {
				exps[key] = e
				order = append(order, key)
			}
			e.accounts = append(e.accounts, label)
			e.input = e.input.Add(v.Input)
			e.output = e.output.Add(v.Output)
			e.balance = e.balance.Add(v.Balance)
			return nil
		})
		if err != nil {
			return rep, fmt.Errorf("read volumes for %q: %w", ledger, err)
		}
	}

	type result struct {
		key string
		bal blnk.Balance
		err error
	}
	results := make([]result, len(order))
	var wg sync.WaitGroup
	sem := make(chan struct{}, opts.Workers)
	for i, key := range order {
		sem <- struct{}{} // at most Workers goroutines at a time
		wg.Add(1)
		go func(i int, e *expectation) {
			defer wg.Done()
			defer func() { <-sem }()
			var b blnk.Balance
			var err error
			if e.balanceID != "" {
				b, err = m.dst.GetBalance(ctx, e.balanceID)
			} else {
				b, err = m.dst.GetBalanceByIndicator(ctx, e.indicator, e.currency)
				if blnk.IsNotFound(err) {
					b, err = blnk.Balance{}, nil // never used: zero balance
				}
			}
			results[i] = result{key: e.target, bal: b, err: err}
		}(i, exps[key])
	}
	wg.Wait()
	// A cancelled run must not be reported as a list of mismatches.
	if err := ctx.Err(); err != nil {
		return rep, err
	}

	for i, key := range order {
		e, r := exps[key], results[i]
		rep.Checked++
		accounts := strings.Join(e.accounts, ", ")
		if r.err != nil {
			rep.Mismatches = append(rep.Mismatches, Mismatch{Target: key, Accounts: accounts, Field: "error", Detail: r.err.Error()})
			continue
		}
		before := len(rep.Mismatches)
		cmp := func(field string, want, got amount.Amount) {
			if want.Cmp(got) != 0 {
				rep.Mismatches = append(rep.Mismatches, Mismatch{
					Target: key, Accounts: accounts, Field: field, Expected: want.String(), Actual: got.String(),
					Detail: "difference " + got.Sub(want).String(),
				})
			}
		}
		cmp("balance", e.balance, r.bal.Balance)
		if opts.CheckVolumes {
			cmp("credit_balance", e.input, r.bal.CreditBalance)
			cmp("debit_balance", e.output, r.bal.DebitBalance)
		}
		if len(rep.Mismatches) == before {
			rep.Matched++
		}
	}
	sort.SliceStable(rep.Mismatches, func(i, j int) bool { return rep.Mismatches[i].Accounts < rep.Mismatches[j].Accounts })
	return rep, nil
}
