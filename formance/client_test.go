package formance_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/otyang/formance2blnk/formance"
	"github.com/otyang/formance2blnk/internal/fakes"
)

func TestTransactionsAscendingAndResume(t *testing.T) {
	for _, desc := range []bool{false, true} {
		f := fakes.NewFormance()
		f.IgnoreReverse = desc
		for i := 0; i < 37; i++ {
			f.Post("l", time.Unix(int64(i), 0), []formance.Posting{{Source: "world", Destination: "a", Amount: fakes.Amt(1), Asset: "USD"}}, nil)
		}
		c, _ := formance.New(formance.Options{BaseURL: f.URL(), PageSize: 5})
		var ids []uint64
		after := uint64(29)
		if err := c.Transactions(context.Background(), "l", &after, func(tx formance.Transaction) error {
			ids = append(ids, tx.ID)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if len(ids) != 7 || ids[0] != 30 || ids[6] != 36 {
			t.Fatalf("desc=%v ids=%v", desc, ids)
		}
		f.Close()
	}
}

func TestTransactionDecoding(t *testing.T) {
	raw := `{"id": 12345678901234, "timestamp":"2025-01-01T00:00:00Z","postings":[{"source":"world","destination":"a","amount":100000000000000000000000,"asset":"USD/2"}],"metadata":{"a":"b","n":{"x":1}},"reverted":true}`
	var tx formance.Transaction
	if err := json.Unmarshal([]byte(raw), &tx); err != nil {
		t.Fatal(err)
	}
	if tx.ID != 12345678901234 || tx.Postings[0].Amount.String() != "100000000000000000000000" || tx.Metadata["n"] != `{"x":1}` || !tx.Reverted {
		t.Fatalf("%+v", tx)
	}
}

func TestOAuthClientCredentials(t *testing.T) {
	var tokenCalls int
	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		tokenCalls++
		r.ParseForm()
		if r.Form.Get("grant_type") != "client_credentials" || r.Form.Get("client_id") != "id" {
			http.Error(w, "bad", 400)
			return
		}
		w.Write([]byte(`{"access_token":"tok","expires_in":3600}`))
	})
	mux.HandleFunc("/api/ledger/v2/l/accounts/a", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "unauthorized", 401)
			return
		}
		w.Write([]byte(`{"data":{"address":"a","metadata":{"k":"v"}}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c, _ := formance.New(formance.Options{BaseURL: srv.URL + "/api/ledger", ClientID: "id", ClientSecret: "s"})
	for i := 0; i < 3; i++ {
		acc, err := c.GetAccount(context.Background(), "l", "a")
		if err != nil || acc.Metadata["k"] != "v" {
			t.Fatalf("%v %+v", err, acc)
		}
	}
	if tokenCalls != 1 {
		t.Fatalf("token fetched %d times, want 1 (cached)", tokenCalls)
	}
}

func TestPaginationRejectsBrokenCursor(t *testing.T) {
	for name, next := range map[string]string{"missing next": "", "repeated cursor": "c1"} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"cursor":{"hasMore":true,"next":"` + next + `","data":[{"account":"a","asset":"USD","balance":1}]}}`))
		}))
		c, _ := formance.New(formance.Options{BaseURL: srv.URL, MaxRetries: -1})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := c.Volumes(ctx, "l", formance.VolumesOptions{}, func(formance.Volume) error { return nil })
		if err == nil || ctx.Err() != nil {
			t.Errorf("%s: expected a cursor error, got %v (ctx %v)", name, err, ctx.Err())
		}
		cancel()
		srv.Close()
	}
}
