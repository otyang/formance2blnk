// Package amount provides an arbitrary-precision integer type for ledger
// amounts that round-trips safely through JSON.
//
// Both Formance and Blnk represent money as integers in minor units (for
// example 10050 for USD 100.50). Those integers can exceed the range of a
// float64 or even an int64, so this package wraps math/big.Int and accepts
// JSON numbers as well as JSON strings when decoding. It always encodes as a
// bare JSON number, which is what both APIs expect.
package amount

import (
	"bytes"
	"fmt"
	"math/big"
)

// Amount is an arbitrary-precision signed integer. The zero value is 0 and is
// ready to use.
type Amount struct {
	v big.Int
}

// New returns an Amount equal to x.
func New(x int64) Amount {
	var a Amount
	a.v.SetInt64(x)
	return a
}

// FromBig returns an Amount holding a copy of b. A nil b yields 0.
func FromBig(b *big.Int) Amount {
	var a Amount
	if b != nil {
		a.v.Set(b)
	}
	return a
}

// Parse parses a base-10 integer string.
func Parse(s string) (Amount, error) {
	var a Amount
	if _, ok := a.v.SetString(s, 10); !ok {
		return Amount{}, fmt.Errorf("amount: invalid integer %q", s)
	}
	return a, nil
}

// MustParse is like Parse but panics on error. Intended for tests and
// constants.
func MustParse(s string) Amount {
	a, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return a
}

// Big returns a copy of the value as a *big.Int.
func (a Amount) Big() *big.Int { return new(big.Int).Set(&a.v) }

// Sign returns -1, 0 or +1.
func (a Amount) Sign() int { return a.v.Sign() }

// IsZero reports whether a == 0.
func (a Amount) IsZero() bool { return a.v.Sign() == 0 }

// Cmp compares a and b and returns -1, 0 or +1.
func (a Amount) Cmp(b Amount) int { return a.v.Cmp(&b.v) }

// Add returns a + b.
func (a Amount) Add(b Amount) Amount {
	var r Amount
	r.v.Add(&a.v, &b.v)
	return r
}

// Sub returns a - b.
func (a Amount) Sub(b Amount) Amount {
	var r Amount
	r.v.Sub(&a.v, &b.v)
	return r
}

// Neg returns -a.
func (a Amount) Neg() Amount {
	var r Amount
	r.v.Neg(&a.v)
	return r
}

// Abs returns |a|.
func (a Amount) Abs() Amount {
	var r Amount
	r.v.Abs(&a.v)
	return r
}

// String returns the base-10 representation.
func (a Amount) String() string { return a.v.String() }

// MarshalJSON encodes the amount as a bare JSON number.
func (a Amount) MarshalJSON() ([]byte, error) {
	return []byte(a.v.String()), nil
}

// UnmarshalJSON accepts a JSON number, a JSON string containing an integer,
// or null (decoded as 0).
func (a *Amount) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		a.v.SetInt64(0)
		return nil
	}
	if b[0] == '"' {
		if len(b) < 2 || b[len(b)-1] != '"' {
			return fmt.Errorf("amount: malformed string %s", b)
		}
		b = b[1 : len(b)-1]
		if len(b) == 0 {
			a.v.SetInt64(0)
			return nil
		}
	}
	s := string(b)
	if _, ok := a.v.SetString(s, 10); !ok {
		// Some servers emit integral values in exponent or decimal form
		// (for example 1e+21 or 100.0). Accept them only if they are exact
		// integers.
		f, _, err := big.ParseFloat(s, 10, 512, big.ToNearestEven)
		if err != nil || !f.IsInt() {
			return fmt.Errorf("amount: %q is not an integer", s)
		}
		f.Int(&a.v)
	}
	return nil
}

// Pow10 returns 10^n as an Amount. It is used to turn a Formance asset
// precision (the "2" in "USD/2") into a Blnk precision multiplier (100).
func Pow10(n int) Amount {
	var a Amount
	a.v.Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
	return a
}
