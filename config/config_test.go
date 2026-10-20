package config

import (
	"strings"
	"testing"
)

func TestExampleIsValid(t *testing.T) {
	if _, err := Parse([]byte(Example)); err != nil {
		t.Fatal(err)
	}
}

func TestEnvExpansionAndRedaction(t *testing.T) {
	t.Setenv("BLNK_KEY_T", "s3cret")
	c, err := Parse([]byte(`{"formance":{"url":"http://f","ledgers":["a"]},
		"blnk":{"url":"${BLNK_URL_T:-http://b}","api_key":"${BLNK_KEY_T}"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Blnk.URL != "http://b" || c.Blnk.APIKey != "s3cret" {
		t.Fatalf("%+v", c.Blnk)
	}
	c.Blnk.URL = "https://user:s3cret@blnk.example"
	if strings.Contains(c.String(), "s3cret") {
		t.Fatal("secret leaked in String()")
	}
}

func TestValidation(t *testing.T) {
	cases := []string{
		`{}`,
		`{"formance":{"url":"x","ledgers":["a"]},"blnk":{"url":"y"},"migration":{"mode":"hybrid"}}`,
		`{"formance":{"url":"x","ledgers":["a"]},"blnk":{"url":"y"},"migration":{"mode":"nope"}}`,
		`{"formance":{"url":"x","ledgers":["a"],"unknown":1},"blnk":{"url":"y"}}`,
		`{"formance":{"url":"x","ledgers":["a"]},"blnk":{"url":"y"},"mapping":{"rules":[{"match":"a"}]}}`,
		`{"formance":{"url":"x","ledgers":["a"]},"blnk":{"url":"y"},"migration":{"batch_size":51}}`,
		`{"formance":{"url":"x","ledgers":["a"]},"blnk":{"url":"y"},"migration":{"batch_size":-1}}`,
		`{"formance":{"url":"x","ledgers":["a"]},"blnk":{"url":"y"},"migration":{"reverts":"skp"}}`,
		`{"formance":{"url":"x","ledgers":["a"]},"blnk":{"url":"y"},"migration":{"on_skipped_account":"ignore"}}`,
		`{"formance":{"url":"x","ledgers":["a"]},"blnk":{"url":"y"},"migration":{"mode":"hybrid","cutoff":"2999-01-01T00:00:00Z"}}`,
		`{"formance":{"url":"x","ledgers":["a"]},"blnk":{"url":"y"},"mapping":{"assets":{"USD/2":{"decimals":-2}}}}`,
	}
	for i, c := range cases {
		if _, err := Parse([]byte(c)); err == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
}
