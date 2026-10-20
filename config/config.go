// Package config loads the JSON configuration used by the formance2blnk CLI
// and turns it into ready-to-use clients and migrator options.
//
// Any string value may reference environment variables as ${NAME} or
// ${NAME:-default}, so secrets never need to live in the file.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/otyang/formance2blnk/blnk"
	"github.com/otyang/formance2blnk/formance"
	"github.com/otyang/formance2blnk/mapping"
	"github.com/otyang/formance2blnk/migrator"
	"github.com/otyang/formance2blnk/state"
)

// Mode selects the migration strategy.
type Mode string

// Supported modes.
const (
	ModeReplay   Mode = "replay"
	ModeSnapshot Mode = "snapshot"
	ModeHybrid   Mode = "hybrid"
)

// Config is the top-level configuration file.
type Config struct {
	Formance  Formance       `json:"formance"`
	Blnk      Blnk           `json:"blnk"`
	Mapping   mapping.Config `json:"mapping"`
	Migration Migration      `json:"migration"`
	StateFile string         `json:"state_file,omitempty"`
}

// Formance holds source connection settings.
type Formance struct {
	URL               string   `json:"url"`
	Ledgers           []string `json:"ledgers"`
	Token             string   `json:"token,omitempty"`
	ClientID          string   `json:"client_id,omitempty"`
	ClientSecret      string   `json:"client_secret,omitempty"`
	TokenURL          string   `json:"token_url,omitempty"`
	Scopes            []string `json:"scopes,omitempty"`
	PageSize          int      `json:"page_size,omitempty"`
	RequestsPerSecond float64  `json:"requests_per_second,omitempty"`
	AscendingParams   string   `json:"ascending_params,omitempty"`
}

// Blnk holds destination connection settings.
type Blnk struct {
	URL               string  `json:"url"`
	APIKey            string  `json:"api_key,omitempty"`
	RequestsPerSecond float64 `json:"requests_per_second,omitempty"`
}

// Migration holds strategy settings. See migrator.Options for semantics.
type Migration struct {
	Mode                Mode   `json:"mode,omitempty"`
	Cutoff              string `json:"cutoff,omitempty"` // RFC 3339, hybrid only
	ReferencePrefix     string `json:"reference_prefix,omitempty"`
	BatchSize           int    `json:"batch_size,omitempty"`
	EnforceBalances     bool   `json:"enforce_balances,omitempty"`
	Reverts             string `json:"reverts,omitempty"`
	OnSkippedAccount    string `json:"on_skipped_account,omitempty"`
	CopyAccountMetadata bool   `json:"copy_account_metadata,omitempty"`
	OpeningIndicator    string `json:"opening_indicator,omitempty"`
	IncludeZeroBalances bool   `json:"include_zero_balances,omitempty"`
}

// Load reads, expands and validates a config file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Parse decodes, expands and validates config JSON.
func Parse(data []byte) (*Config, error) {
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	expanded, err := json.Marshal(expandAll(raw))
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(expanded))
	dec.DisallowUnknownFields()
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	c.applyDefaults()
	return &c, c.Validate()
}

func (c *Config) applyDefaults() {
	if c.Migration.Mode == "" {
		c.Migration.Mode = ModeReplay
	}
	if c.StateFile == "" {
		c.StateFile = "formance2blnk-state.json"
	}
}

// Validate reports configuration errors.
func (c *Config) Validate() error {
	var errs []error
	if c.Formance.URL == "" {
		errs = append(errs, errors.New("formance.url is required"))
	}
	if len(c.Formance.Ledgers) == 0 {
		errs = append(errs, errors.New("formance.ledgers must list at least one ledger"))
	}
	if c.Formance.Token != "" && c.Formance.ClientID != "" {
		errs = append(errs, errors.New("formance: set either token or client_id/client_secret, not both"))
	}
	if c.Blnk.URL == "" {
		errs = append(errs, errors.New("blnk.url is required"))
	}
	switch c.Migration.Mode {
	case ModeReplay, ModeSnapshot:
	case ModeHybrid:
		if t, err := c.CutoffTime(); err != nil {
			errs = append(errs, err)
		} else if t.After(time.Now()) {
			errs = append(errs, fmt.Errorf("migration.cutoff %s is in the future", c.Migration.Cutoff))
		}
	default:
		errs = append(errs, fmt.Errorf("migration.mode %q must be replay, snapshot or hybrid", c.Migration.Mode))
	}
	if c.Migration.BatchSize < 0 || c.Migration.BatchSize > blnk.MaxAtomicBulk {
		errs = append(errs, fmt.Errorf("migration.batch_size %d must be between 1 and %d (0 for the default)", c.Migration.BatchSize, blnk.MaxAtomicBulk))
	}
	switch migrator.RevertPolicy(c.Migration.Reverts) {
	case "", migrator.RevertReplay, migrator.RevertSkip:
	default:
		errs = append(errs, fmt.Errorf("migration.reverts %q must be replay or skip", c.Migration.Reverts))
	}
	switch migrator.SkippedAccountPolicy(c.Migration.OnSkippedAccount) {
	case "", migrator.SkippedAccountError, migrator.SkippedAccountDrop:
	default:
		errs = append(errs, fmt.Errorf("migration.on_skipped_account %q must be error or drop", c.Migration.OnSkippedAccount))
	}
	if _, err := mapping.New(c.Mapping); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// CutoffTime parses migration.cutoff.
func (c *Config) CutoffTime() (time.Time, error) {
	if c.Migration.Cutoff == "" {
		return time.Time{}, errors.New("migration.cutoff is required for hybrid mode")
	}
	t, err := time.Parse(time.RFC3339Nano, c.Migration.Cutoff)
	if err != nil {
		return time.Time{}, fmt.Errorf("migration.cutoff: %w", err)
	}
	return t, nil
}

// FormanceClient builds the source client.
func (c *Config) FormanceClient() (*formance.Client, error) {
	return formance.New(formance.Options{
		BaseURL: c.Formance.URL, BearerToken: c.Formance.Token,
		ClientID: c.Formance.ClientID, ClientSecret: c.Formance.ClientSecret,
		TokenURL: c.Formance.TokenURL, Scopes: c.Formance.Scopes,
		PageSize: c.Formance.PageSize, RequestsPerSecond: c.Formance.RequestsPerSecond,
		AscendingParams: c.Formance.AscendingParams,
	})
}

// BlnkClient builds the destination client.
func (c *Config) BlnkClient() (*blnk.Client, error) {
	return blnk.New(blnk.Options{BaseURL: c.Blnk.URL, APIKey: c.Blnk.APIKey, RequestsPerSecond: c.Blnk.RequestsPerSecond})
}

// MigratorOptions builds migrator options (without Store, which the caller
// opens so it can close it).
func (c *Config) MigratorOptions(logger *slog.Logger) (migrator.Options, error) {
	m, err := mapping.New(c.Mapping)
	if err != nil {
		return migrator.Options{}, err
	}
	return migrator.Options{
		Ledgers: c.Formance.Ledgers, Mapper: m, Logger: logger,
		ReferencePrefix:     c.Migration.ReferencePrefix,
		BatchSize:           c.Migration.BatchSize,
		EnforceBalances:     c.Migration.EnforceBalances,
		Reverts:             migrator.RevertPolicy(c.Migration.Reverts),
		OnSkippedAccount:    migrator.SkippedAccountPolicy(c.Migration.OnSkippedAccount),
		CopyAccountMetadata: c.Migration.CopyAccountMetadata,
		OpeningIndicator:    c.Migration.OpeningIndicator,
		IncludeZeroBalances: c.Migration.IncludeZeroBalances,
	}, nil
}

// OpenState opens the configured state file.
func (c *Config) OpenState() (*state.File, error) { return state.OpenFile(c.StateFile) }

var envRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:-([^}]*))?\}`)

// ExpandEnv replaces ${NAME} and ${NAME:-default} with environment values.
func ExpandEnv(s string) string {
	return envRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := envRe.FindStringSubmatch(m)
		if v, ok := os.LookupEnv(sub[1]); ok && v != "" {
			return v
		}
		return sub[3]
	})
}

func expandAll(v any) any {
	switch t := v.(type) {
	case string:
		return ExpandEnv(t)
	case []any:
		for i := range t {
			t[i] = expandAll(t[i])
		}
		return t
	case map[string]any:
		for k := range t {
			t[k] = expandAll(t[k])
		}
		return t
	}
	return v
}

// Example is a commented starting configuration (comments are not JSON, so
// they live in the docs; this is valid JSON).
const Example = `{
  "formance": {
    "url": "http://localhost:3068",
    "ledgers": ["main"],
    "token": "${FORMANCE_TOKEN:-}",
    "page_size": 100
  },
  "blnk": {
    "url": "http://localhost:5001",
    "api_key": "${BLNK_API_KEY:-}"
  },
  "mapping": {
    "world_indicator": "@World",
    "default_ledger": "{ledger}",
    "rules": [
      { "match": "platform:fees", "indicator": "@Fees" },
      { "match": "users:*:wallet", "ledger": "Customer Wallets" },
      { "match": "merchants:**", "ledger": "Merchants" }
    ]
  },
  "migration": {
    "mode": "replay",
    "batch_size": 50,
    "reverts": "replay",
    "on_skipped_account": "error",
    "copy_account_metadata": true
  },
  "state_file": "formance2blnk-state.json"
}
`

// String is a helper for printing redacted configs.
func (c Config) String() string {
	c.Formance.Token = redact(c.Formance.Token)
	c.Formance.ClientSecret = redact(c.Formance.ClientSecret)
	c.Blnk.APIKey = redact(c.Blnk.APIKey)
	c.Formance.URL = redactURL(c.Formance.URL)
	c.Formance.TokenURL = redactURL(c.Formance.TokenURL)
	c.Blnk.URL = redactURL(c.Blnk.URL)
	b, _ := json.MarshalIndent(c, "", "  ")
	return string(b)
}

func redact(s string) string {
	if s == "" {
		return ""
	}
	return strings.Repeat("*", 8)
}

// redactURL hides a password embedded in a URL's userinfo.
func redactURL(s string) string {
	u, err := url.Parse(s)
	if err != nil || u.User == nil {
		return s
	}
	return u.Redacted()
}
