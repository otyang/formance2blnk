package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/otyang/formance2blnk/formance"
	"github.com/otyang/formance2blnk/internal/fakes"
)

func TestCLIEndToEnd(t *testing.T) {
	f, b := fakes.NewFormance(), fakes.NewBlnk()
	defer f.Close()
	defer b.Close()
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 30; i++ {
		f.Post("main", base.Add(time.Duration(i)*time.Hour), []formance.Posting{
			{Source: "world", Destination: fmt.Sprintf("users:%d:wallet", i%5), Amount: fakes.Amt(int64(100 + i)), Asset: "USD/2"},
		}, nil)
	}
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "cfg.json")
	cfg := fmt.Sprintf(`{"formance":{"url":%q,"ledgers":["main"]},"blnk":{"url":%q},
		"mapping":{"rules":[{"match":"users:*:wallet","ledger":"Wallets"}]},
		"state_file":%q}`, f.URL(), b.URL(), filepath.Join(dir, "state.json"))
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	exec := func(args ...string) (int, string) {
		var out, errb bytes.Buffer
		code := run(ctx, args, &out, &errb)
		return code, out.String() + errb.String()
	}

	if code, out := exec("validate", "-config", cfgPath); code != 0 {
		t.Fatalf("validate: %d %s", code, out)
	}
	if code, out := exec("plan", "-config", cfgPath, "-log-level", "warn"); code != 0 || !strings.Contains(out, "plan only") {
		t.Fatalf("plan: %d %s", code, out)
	}
	if b.TransactionCount() != 0 {
		t.Fatal("plan wrote transactions")
	}
	if code, out := exec("migrate", "-config", cfgPath, "-log-level", "warn", "-json"); code != 0 {
		t.Fatalf("migrate: %d %s", code, out)
	}
	if b.TransactionCount() != 30 {
		t.Fatalf("blnk has %d transactions", b.TransactionCount())
	}
	code, out := exec("reconcile", "-config", cfgPath, "-volumes", "-format", "json", "-log-level", "warn")
	if code != 0 {
		t.Fatalf("reconcile: %d %s", code, out)
	}
	var rep struct{ Checked, Matched int }
	if err := json.Unmarshal([]byte(out), &rep); err != nil || rep.Checked == 0 || rep.Checked != rep.Matched {
		t.Fatalf("report %s (%v)", out, err)
	}

	// Drift → exit code 2.
	f.Post("main", base.Add(999*time.Hour), []formance.Posting{{Source: "world", Destination: "users:1:wallet", Amount: fakes.Amt(1), Asset: "USD/2"}}, nil)
	if code, out := exec("reconcile", "-config", cfgPath, "-log-level", "warn"); code != exitMismatch || !strings.Contains(out, "MISMATCH") {
		t.Fatalf("expected mismatch exit, got %d %s", code, out)
	}
}

func TestCLIUsage(t *testing.T) {
	var out, errb bytes.Buffer
	if run(context.Background(), nil, &out, &errb) != exitUsage {
		t.Fatal("expected usage exit")
	}
	if run(context.Background(), []string{"init"}, &out, &errb) != exitOK || !strings.Contains(out.String(), `"formance"`) {
		t.Fatal("init failed")
	}
	if run(context.Background(), []string{"bogus"}, &out, &errb) != exitUsage {
		t.Fatal("expected usage exit for unknown command")
	}
}
