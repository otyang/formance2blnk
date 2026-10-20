package mapping_test

import (
	"fmt"
	"strings"

	"github.com/otyang/formance2blnk/mapping"
)

func ExampleNew() {
	m, _ := mapping.New(mapping.Config{
		DefaultLedger: "Formance {ledger}",
		Rules: []mapping.Rule{
			{Match: "platform:fees", Indicator: "@Fees"},
			{Match: "banks:*", Indicator: "@Bank-{1}"},
			{Match: "users:*:wallet", Ledger: "Customer Wallets"},
			{Match: "test:**", Skip: true},
		},
	})
	for _, addr := range []string{"world", "platform:fees", "banks:gtbank", "users:42:wallet", "orders:9", "test:a:b"} {
		t, _ := m.Account("main", addr)
		fmt.Printf("%-16s -> %s\n", addr, strings.TrimSpace(t.Kind.String()+" "+t.Ledger+t.Indicator))
	}
	a, _ := m.Asset("USD/2")
	fmt.Println(a.Currency, a.Precision)
	// Output:
	// world            -> indicator @World
	// platform:fees    -> indicator @Fees
	// banks:gtbank     -> indicator @Bank-gtbank
	// users:42:wallet  -> balance Customer Wallets
	// orders:9         -> balance Formance main
	// test:a:b         -> skip
	// USD 100
}
