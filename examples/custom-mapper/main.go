// Custom-mapper shows how to plug your own account-routing logic into the
// migrator by implementing mapping.Mapper. Here, accounts are routed to a
// Blnk ledger per country taken from the address, and any asset ending in
// "_TEST" is mapped to a sandbox currency.
//
//	go run ./examples/custom-mapper
package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/otyang/formance2blnk/blnk"
	"github.com/otyang/formance2blnk/formance"
	"github.com/otyang/formance2blnk/internal/fakes"
	"github.com/otyang/formance2blnk/mapping"
	"github.com/otyang/formance2blnk/migrator"
)

// countryMapper routes "users:<country>:<id>" to "Wallets <COUNTRY>" and
// falls back to the rules mapper for everything else.
type countryMapper struct {
	fallback *mapping.Rules
}

func (c countryMapper) Account(ledger, address string) (mapping.Target, error) {
	parts := strings.Split(address, ":")
	if len(parts) == 3 && parts[0] == "users" {
		return mapping.Target{Kind: mapping.KindBalance, Ledger: "Wallets " + strings.ToUpper(parts[1])}, nil
	}
	return c.fallback.Account(ledger, address)
}

func (c countryMapper) Asset(asset string) (mapping.Asset, error) {
	a, err := c.fallback.Asset(asset)
	if err == nil && strings.HasSuffix(a.Currency, "_TEST") {
		a.Currency = "SANDBOX"
	}
	return a, err
}

func main() {
	fm, bk := fakes.NewFormance(), fakes.NewBlnk()
	defer fm.Close()
	defer bk.Close()
	now := time.Now()
	fm.Post("main", now, []formance.Posting{{Source: "world", Destination: "users:ng:1", Amount: fakes.Amt(500000), Asset: "NGN/2"}}, nil)
	fm.Post("main", now, []formance.Posting{{Source: "world", Destination: "users:gh:7", Amount: fakes.Amt(9000), Asset: "GHS/2"}}, nil)

	rules, _ := mapping.New(mapping.Config{})
	src, _ := formance.New(formance.Options{BaseURL: fm.URL()})
	dst, _ := blnk.New(blnk.Options{BaseURL: bk.URL()})
	m, err := migrator.New(src, dst, migrator.Options{Ledgers: []string{"main"}, Mapper: countryMapper{rules}})
	if err != nil {
		panic(err)
	}
	st, err := m.Replay(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Printf("created %d ledgers and %d balances\n", st.LedgersCreated, st.BalancesCreated)
}
