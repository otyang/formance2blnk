// Package mapping decides where each Formance account and asset lands in
// Blnk.
//
// A Formance account (for example "users:42:wallet") becomes either:
//   - a regular Blnk balance inside a named Blnk ledger (one balance per
//     asset, since Blnk balances are single-currency), or
//   - a Blnk internal balance addressed by indicator (for example "@World"),
//     which Blnk creates on first use, or
//   - nothing (Skip), in which case postings touching it are rejected.
//
// Rules are evaluated in order; the first match wins. Patterns are matched
// segment-by-segment on the ":"-separated address:
//
//	"*"      matches exactly one segment
//	"**"     matches zero or more trailing segments (must be last)
//	"user_*" any path.Match glob within one segment
//
// Templates in Ledger and Indicator may use {ledger} (the source Formance
// ledger), {0} (the full address) and {1}, {2}, … (wildcard captures).
package mapping

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/otyang/formance2blnk/amount"
)

// Kind is the type of mapping target.
type Kind int

const (
	// KindBalance maps to a regular balance in a Blnk ledger.
	KindBalance Kind = iota
	// KindIndicator maps to a Blnk internal balance ("@Name").
	KindIndicator
	// KindSkip excludes the account from migration.
	KindSkip
)

func (k Kind) String() string {
	switch k {
	case KindBalance:
		return "balance"
	case KindIndicator:
		return "indicator"
	case KindSkip:
		return "skip"
	}
	return "unknown"
}

// Target is where an account maps to.
type Target struct {
	Kind      Kind
	Ledger    string // Blnk ledger name, for KindBalance
	Indicator string // "@Name", for KindIndicator
}

// Asset is a Formance asset translated to Blnk terms.
type Asset struct {
	Source    string        // original Formance asset, e.g. "USD/2"
	Currency  string        // Blnk currency, e.g. "USD"
	Decimals  int           // 2
	Precision amount.Amount // Blnk precision multiplier, e.g. 100
}

// Mapper resolves accounts and assets. Implement it to plug custom logic
// into the migrator; Rules is the configurable default.
type Mapper interface {
	Account(sourceLedger, address string) (Target, error)
	Asset(asset string) (Asset, error)
}

// Rule is one mapping rule. Exactly one of Ledger, Indicator or Skip should
// be set.
type Rule struct {
	Match     string `json:"match"`
	Ledger    string `json:"ledger,omitempty"`
	Indicator string `json:"indicator,omitempty"`
	Skip      bool   `json:"skip,omitempty"`
}

// AssetOverride customises how a Formance asset is translated.
type AssetOverride struct {
	Currency string `json:"currency,omitempty"`
	Decimals *int   `json:"decimals,omitempty"`
}

// Config configures the rules-based Mapper.
type Config struct {
	// Rules are evaluated in order before the defaults below.
	Rules []Rule `json:"rules,omitempty"`
	// WorldIndicator is used for Formance's "world" account. Default "@World".
	WorldIndicator string `json:"world_indicator,omitempty"`
	// DefaultLedger is the Blnk ledger name for unmatched accounts.
	// Default "{ledger}" (same name as the Formance ledger).
	DefaultLedger string `json:"default_ledger,omitempty"`
	// Assets overrides keyed by Formance asset ("USD/2").
	Assets map[string]AssetOverride `json:"assets,omitempty"`
}

// Rules is the default Mapper implementation.
type Rules struct {
	cfg      Config
	compiled []compiledRule
}

type compiledRule struct {
	Rule
	segs []string
}

// New validates cfg and returns a Mapper.
func New(cfg Config) (*Rules, error) {
	if cfg.WorldIndicator == "" {
		cfg.WorldIndicator = "@World"
	}
	if cfg.DefaultLedger == "" {
		cfg.DefaultLedger = "{ledger}"
	}
	if !strings.HasPrefix(cfg.WorldIndicator, "@") {
		return nil, fmt.Errorf("mapping: world_indicator %q must start with @", cfg.WorldIndicator)
	}
	for asset, o := range cfg.Assets {
		if o.Decimals != nil && (*o.Decimals < 0 || *o.Decimals > 30) {
			return nil, fmt.Errorf("mapping: asset %q: decimals %d must be between 0 and 30", asset, *o.Decimals)
		}
	}
	r := &Rules{cfg: cfg}
	for i, rule := range cfg.Rules {
		set := 0
		if rule.Ledger != "" {
			set++
		}
		if rule.Indicator != "" {
			set++
			if !strings.HasPrefix(rule.Indicator, "@") {
				return nil, fmt.Errorf("mapping: rule %d: indicator %q must start with @", i, rule.Indicator)
			}
		}
		if rule.Skip {
			set++
		}
		if set != 1 {
			return nil, fmt.Errorf("mapping: rule %d (%q): set exactly one of ledger, indicator or skip", i, rule.Match)
		}
		if rule.Match == "" {
			return nil, fmt.Errorf("mapping: rule %d: match is required", i)
		}
		segs := strings.Split(rule.Match, ":")
		for j, s := range segs {
			if s == "**" && j != len(segs)-1 {
				return nil, fmt.Errorf("mapping: rule %d: ** must be the last segment", i)
			}
			if _, err := path.Match(s, ""); err != nil {
				return nil, fmt.Errorf("mapping: rule %d: bad pattern %q: %w", i, s, err)
			}
		}
		r.compiled = append(r.compiled, compiledRule{Rule: rule, segs: segs})
	}
	return r, nil
}

// Account implements Mapper.
func (r *Rules) Account(sourceLedger, address string) (Target, error) {
	for _, rule := range r.compiled {
		caps, ok := match(rule.segs, strings.Split(address, ":"))
		if !ok {
			continue
		}
		vars := templateVars(sourceLedger, address, caps)
		switch {
		case rule.Skip:
			return Target{Kind: KindSkip}, nil
		case rule.Indicator != "":
			return Target{Kind: KindIndicator, Indicator: expand(rule.Indicator, vars)}, nil
		default:
			return Target{Kind: KindBalance, Ledger: expand(rule.Ledger, vars)}, nil
		}
	}
	if address == "world" {
		return Target{Kind: KindIndicator, Indicator: r.cfg.WorldIndicator}, nil
	}
	return Target{Kind: KindBalance, Ledger: expand(r.cfg.DefaultLedger, templateVars(sourceLedger, address, nil))}, nil
}

var assetRe = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_]*)(?:/(\d+))?$`)

// ErrInvalidAsset is returned for assets that cannot be translated.
var ErrInvalidAsset = errors.New("mapping: invalid asset")

// Asset implements Mapper.
func (r *Rules) Asset(asset string) (Asset, error) {
	m := assetRe.FindStringSubmatch(asset)
	if m == nil {
		return Asset{}, fmt.Errorf("%w %q", ErrInvalidAsset, asset)
	}
	a := Asset{Source: asset, Currency: m[1]}
	if m[2] != "" {
		d, err := strconv.Atoi(m[2])
		if err != nil || d > 30 {
			return Asset{}, fmt.Errorf("%w %q: unsupported precision", ErrInvalidAsset, asset)
		}
		a.Decimals = d
	}
	if o, ok := r.cfg.Assets[asset]; ok {
		if o.Currency != "" {
			a.Currency = o.Currency
		}
		if o.Decimals != nil {
			a.Decimals = *o.Decimals
		}
	}
	a.Precision = amount.Pow10(a.Decimals)
	return a, nil
}

func match(pat, addr []string) ([]string, bool) {
	var caps []string
	for i, p := range pat {
		if p == "**" {
			caps = append(caps, strings.Join(addr[i:], ":"))
			return caps, true
		}
		if i >= len(addr) {
			return nil, false
		}
		if p == addr[i] {
			continue
		}
		ok, _ := path.Match(p, addr[i])
		if !ok {
			return nil, false
		}
		caps = append(caps, addr[i])
	}
	return caps, len(pat) == len(addr)
}

func templateVars(ledger, address string, caps []string) map[string]string {
	v := map[string]string{"ledger": ledger, "0": address}
	for i, c := range caps {
		v[strconv.Itoa(i+1)] = c
	}
	return v
}

var placeholder = regexp.MustCompile(`\{([a-z0-9]+)\}`)

func expand(tpl string, vars map[string]string) string {
	return placeholder.ReplaceAllStringFunc(tpl, func(m string) string {
		if v, ok := vars[m[1:len(m)-1]]; ok {
			return v
		}
		return m
	})
}
