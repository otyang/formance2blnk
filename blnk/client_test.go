package blnk_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/otyang/formance2blnk/amount"
	"github.com/otyang/formance2blnk/blnk"
	"github.com/otyang/formance2blnk/internal/fakes"
)

func TestDuplicateReferenceAndKey(t *testing.T) {
	b := fakes.NewBlnk()
	defer b.Close()
	c, _ := blnk.New(blnk.Options{BaseURL: b.URL()})
	ctx := context.Background()
	l, err := c.CreateLedger(ctx, "L", nil)
	if err != nil {
		t.Fatal(err)
	}
	bal, err := c.CreateBalance(ctx, blnk.BalanceRequest{LedgerID: l.LedgerID, Currency: "USD"})
	if err != nil {
		t.Fatal(err)
	}
	req := blnk.TransactionRequest{PreciseAmount: amount.New(150), Precision: amount.New(100), Reference: "r1",
		Currency: "USD", Source: "@World", Destination: bal.BalanceID, AllowOverdraft: true, SkipQueue: true}
	if _, err := c.CreateTransaction(ctx, req); err != nil {
		t.Fatal(err)
	}
	_, err = c.CreateTransaction(ctx, req)
	if !blnk.IsDuplicateReference(err) {
		t.Fatalf("expected duplicate, got %v", err)
	}
	got, _ := c.GetBalance(ctx, bal.BalanceID)
	if got.Balance.String() != "150" || got.CreditBalance.String() != "150" {
		t.Fatalf("%+v", got)
	}
	w, err := c.GetBalanceByIndicator(ctx, "@World", "USD")
	if err != nil || w.Balance.String() != "-150" {
		t.Fatalf("%v %+v", err, w)
	}
}

func TestAPIKeyHeaderAndRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Blnk-Key") != "secret" {
			http.Error(w, "unauthorized", 401)
			return
		}
		w.Write([]byte(`{"transaction_id":"t","reference":"r","status":"REJECTED"}`))
	}))
	defer srv.Close()
	c, _ := blnk.New(blnk.Options{BaseURL: srv.URL, APIKey: "secret"})
	_, err := c.CreateTransaction(context.Background(), blnk.TransactionRequest{Reference: "r"})
	var rej *blnk.RejectedError
	if err == nil || !asRejected(err, &rej) {
		t.Fatalf("expected RejectedError, got %v", err)
	}
}

func asRejected(err error, target **blnk.RejectedError) bool {
	r, ok := err.(*blnk.RejectedError)
	*target = r
	return ok
}

func TestAtomicLimit(t *testing.T) {
	c, _ := blnk.New(blnk.Options{BaseURL: "http://unused"})
	_, err := c.CreateBulk(context.Background(), blnk.BulkRequest{Atomic: true, Transactions: make([]blnk.TransactionRequest, 51)})
	if err == nil {
		t.Fatal("expected atomic limit error")
	}
}
