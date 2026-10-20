package amount

import (
	"encoding/json"
	"testing"
)

func TestUnmarshalForms(t *testing.T) {
	cases := map[string]string{
		`123`:                         "123",
		`"456"`:                       "456",
		`-7`:                          "-7",
		`null`:                        "0",
		`""`:                          "0",
		`1e+3`:                        "1000",
		`100.0`:                       "100",
		`123456789012345678901234567`: "123456789012345678901234567",
	}
	for in, want := range cases {
		var a Amount
		if err := json.Unmarshal([]byte(in), &a); err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if a.String() != want {
			t.Errorf("%s: got %s want %s", in, a, want)
		}
	}
}

func TestUnmarshalRejectsFractions(t *testing.T) {
	var a Amount
	if err := json.Unmarshal([]byte(`1.5`), &a); err == nil {
		t.Fatal("expected error for fractional amount")
	}
}

func TestMarshalIsBareNumber(t *testing.T) {
	b, _ := json.Marshal(struct {
		A Amount `json:"a"`
	}{MustParse("10050")})
	if string(b) != `{"a":10050}` {
		t.Fatalf("got %s", b)
	}
}

func TestArithmetic(t *testing.T) {
	a, b := New(10), New(3)
	if a.Add(b).String() != "13" || a.Sub(b).String() != "7" || b.Sub(a).Abs().String() != "7" || a.Neg().Sign() != -1 {
		t.Fatal("arithmetic mismatch")
	}
	if Pow10(2).String() != "100" || Pow10(0).String() != "1" {
		t.Fatal("pow10 mismatch")
	}
}
