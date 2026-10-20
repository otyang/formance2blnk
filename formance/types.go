package formance

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/otyang/formance2blnk/amount"
)

// MetadataRevertsKey is the transaction metadata key Formance sets on a
// transaction that reverts another one. Its value is the reverted
// transaction's ID.
const MetadataRevertsKey = "com.formance.spec/state/reverts"

// Posting is a single movement of value inside a Formance transaction.
type Posting struct {
	Source      string        `json:"source"`
	Destination string        `json:"destination"`
	Amount      amount.Amount `json:"amount"`
	Asset       string        `json:"asset"`
}

// Transaction is a Formance Ledger v2 transaction.
type Transaction struct {
	ID         uint64     `json:"-"`
	Timestamp  time.Time  `json:"timestamp"`
	InsertedAt *time.Time `json:"insertedAt,omitempty"`
	Postings   []Posting  `json:"postings"`
	Metadata   Metadata   `json:"metadata"`
	Reference  string     `json:"reference,omitempty"`
	Reverted   bool       `json:"reverted"`
	RevertedAt *time.Time `json:"revertedAt,omitempty"`
}

// UnmarshalJSON decodes the transaction, accepting the ID as either a JSON
// number or a string (Formance encodes it as a big integer).
func (t *Transaction) UnmarshalJSON(b []byte) error {
	type alias Transaction
	aux := struct {
		*alias
		ID json.RawMessage `json:"id"`
	}{alias: (*alias)(t)}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	id, err := parseID(aux.ID)
	if err != nil {
		return fmt.Errorf("transaction id: %w", err)
	}
	t.ID = id
	return nil
}

// MarshalJSON encodes the transaction including its numeric ID.
func (t Transaction) MarshalJSON() ([]byte, error) {
	type alias Transaction
	return json.Marshal(struct {
		ID uint64 `json:"id"`
		alias
	}{t.ID, alias(t)})
}

// RevertsID returns the ID of the transaction this one reverts, if any.
func (t Transaction) RevertsID() (uint64, bool) {
	v, ok := t.Metadata[MetadataRevertsKey]
	if !ok || v == "" {
		return 0, false
	}
	id, err := strconv.ParseUint(v, 10, 64)
	return id, err == nil
}

func parseID(raw json.RawMessage) (uint64, error) {
	s := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if s == "" || s == "null" {
		return 0, nil
	}
	return strconv.ParseUint(s, 10, 64)
}

// Metadata is Formance metadata. v2 metadata values are strings; older
// ledgers may contain arbitrary JSON, which is stringified on decode.
type Metadata map[string]string

// UnmarshalJSON tolerates non-string values.
func (m *Metadata) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	out := make(Metadata, len(raw))
	for k, v := range raw {
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			out[k] = s
			continue
		}
		out[k] = string(v)
	}
	*m = out
	return nil
}

// Volume is the aggregated movement of one asset on one account.
type Volume struct {
	Account string        `json:"account"`
	Asset   string        `json:"asset"`
	Input   amount.Amount `json:"input"`
	Output  amount.Amount `json:"output"`
	Balance amount.Amount `json:"balance"`
}

// Account is a Formance account with its metadata.
type Account struct {
	Address  string   `json:"address"`
	Metadata Metadata `json:"metadata"`
}

// Ledger is a Formance ledger descriptor.
type Ledger struct {
	Name   string `json:"name"`
	Bucket string `json:"bucket,omitempty"`
}

type cursor[T any] struct {
	Cursor struct {
		PageSize int    `json:"pageSize"`
		HasMore  bool   `json:"hasMore"`
		Previous string `json:"previous"`
		Next     string `json:"next"`
		Data     []T    `json:"data"`
	} `json:"cursor"`
}

type single[T any] struct {
	Data T `json:"data"`
}
