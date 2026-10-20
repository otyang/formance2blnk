package mapping

import "testing"

func TestRules(t *testing.T) {
	m, err := New(Config{
		Rules: []Rule{
			{Match: "platform:fees", Indicator: "@Fees"},
			{Match: "banks:*", Indicator: "@Bank-{1}"},
			{Match: "users:*:wallet", Ledger: "Customer Wallets"},
			{Match: "merchants:**", Ledger: "Merchants ({ledger})"},
			{Match: "tmp:*", Skip: true},
		},
		DefaultLedger: "Formance {ledger}",
	})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		addr string
		want Target
	}{
		{"world", Target{Kind: KindIndicator, Indicator: "@World"}},
		{"platform:fees", Target{Kind: KindIndicator, Indicator: "@Fees"}},
		{"banks:gtbank", Target{Kind: KindIndicator, Indicator: "@Bank-gtbank"}},
		{"users:42:wallet", Target{Kind: KindBalance, Ledger: "Customer Wallets"}},
		{"users:42:savings", Target{Kind: KindBalance, Ledger: "Formance main"}},
		{"merchants:9:pending:x", Target{Kind: KindBalance, Ledger: "Merchants (main)"}},
		{"merchants", Target{Kind: KindBalance, Ledger: "Merchants (main)"}},
		{"tmp:1", Target{Kind: KindSkip}},
	}
	for _, c := range cases {
		got, err := m.Account("main", c.addr)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("%s: got %+v want %+v", c.addr, got, c.want)
		}
	}
}

func TestAssets(t *testing.T) {
	two := 2
	m, _ := New(Config{Assets: map[string]AssetOverride{"COIN": {Currency: "NGN", Decimals: &two}}})
	cases := map[string][2]string{
		"USD/2": {"USD", "100"},
		"BTC/8": {"BTC", "100000000"},
		"JPY":   {"JPY", "1"},
		"COIN":  {"NGN", "100"},
	}
	for in, want := range cases {
		a, err := m.Asset(in)
		if err != nil {
			t.Fatal(err)
		}
		if a.Currency != want[0] || a.Precision.String() != want[1] {
			t.Errorf("%s: got %s/%s", in, a.Currency, a.Precision)
		}
	}
	if _, err := m.Asset("US D/2"); err == nil {
		t.Error("expected invalid asset error")
	}
}

func TestInvalidRules(t *testing.T) {
	bad := []Config{
		{Rules: []Rule{{Match: "a", Ledger: "x", Skip: true}}},
		{Rules: []Rule{{Match: "a", Indicator: "NoAt"}}},
		{Rules: []Rule{{Match: "**:a", Ledger: "x"}}},
		{WorldIndicator: "World"},
	}
	for i, c := range bad {
		if _, err := New(c); err == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
}
