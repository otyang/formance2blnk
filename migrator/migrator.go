// Package migrator moves data from a Formance ledger into Blnk.
//
// It supports three strategies:
//
//   - Replay: every Formance transaction is re-recorded in Blnk, in ID order,
//     with its original timestamp as the effective date. Full audit history.
//   - Snapshot: one opening-balance transaction per (account, asset), taken
//     from Formance volumes, optionally as of a point in time.
//   - Hybrid: a snapshot as of a cutoff, followed by a replay of everything
//     effective at or after the cutoff.
//
// All writes use deterministic references, so any run can be interrupted and
// restarted without double-posting. Progress is checkpointed in a
// state.Store, and Reconcile compares the two ledgers afterwards.
package migrator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/otyang/formance2blnk/blnk"
	"github.com/otyang/formance2blnk/formance"
	"github.com/otyang/formance2blnk/mapping"
	"github.com/otyang/formance2blnk/state"
)

// Source is the read side (implemented by *formance.Client).
type Source interface {
	Transactions(ctx context.Context, ledger string, afterID *uint64, fn func(formance.Transaction) error) error
	Volumes(ctx context.Context, ledger string, opts formance.VolumesOptions, fn func(formance.Volume) error) error
	GetAccount(ctx context.Context, ledger, address string) (formance.Account, error)
}

// Destination is the write side (implemented by *blnk.Client).
type Destination interface {
	CreateLedger(ctx context.Context, name string, meta map[string]any) (blnk.Ledger, error)
	CreateBalance(ctx context.Context, req blnk.BalanceRequest) (blnk.Balance, error)
	GetBalance(ctx context.Context, id string) (blnk.Balance, error)
	GetBalanceByIndicator(ctx context.Context, indicator, currency string) (blnk.Balance, error)
	CreateTransaction(ctx context.Context, req blnk.TransactionRequest) (blnk.Transaction, error)
	CreateBulk(ctx context.Context, req blnk.BulkRequest) (blnk.BulkResult, error)
}

// RevertPolicy controls how reverted Formance transactions are handled.
type RevertPolicy string

const (
	// RevertReplay replays both the original and its revert (default; the
	// Blnk history mirrors Formance exactly).
	RevertReplay RevertPolicy = "replay"
	// RevertSkip drops reverted transactions and their reverts. Net balances
	// are unchanged; credit/debit volumes will differ from Formance.
	RevertSkip RevertPolicy = "skip"
)

// SkippedAccountPolicy controls postings that touch an account mapped with
// mapping.KindSkip.
type SkippedAccountPolicy string

const (
	// SkippedAccountError aborts the migration (default, safest).
	SkippedAccountError SkippedAccountPolicy = "error"
	// SkippedAccountDrop silently drops the posting.
	SkippedAccountDrop SkippedAccountPolicy = "drop"
)

// TxContext describes the Formance posting a Blnk request was built from.
type TxContext struct {
	SourceLedger string
	Transaction  formance.Transaction
	PostingIndex int
	Posting      formance.Posting
}

// Options configures a Migrator.
type Options struct {
	// Ledgers are the Formance ledgers to migrate. Required.
	Ledgers []string
	Mapper  mapping.Mapper
	Store   state.Store
	Logger  *slog.Logger

	// ReferencePrefix prefixes every Blnk reference. Default "fm". Change it
	// only if you deliberately want a second, independent copy.
	ReferencePrefix string
	// BatchSize is the number of Blnk transactions per atomic bulk call
	// (1..50). Default 50. Use 1 to send transactions one by one.
	BatchSize int
	// EnforceBalances disables allow_overdraft on replayed transactions.
	// Formance already validated history, so the default (false) is
	// recommended.
	EnforceBalances bool
	// Reverts selects the revert policy. Default RevertReplay.
	Reverts RevertPolicy
	// OnSkippedAccount selects the policy for postings touching skipped
	// accounts. Default SkippedAccountError.
	OnSkippedAccount SkippedAccountPolicy
	// CopyAccountMetadata copies Formance account metadata onto new Blnk
	// balances (one extra GET per account).
	CopyAccountMetadata bool
	// OpeningIndicator is the counter-party for opening balances in snapshot
	// mode. Default: the mapper's world target ("@World"), which makes the
	// Blnk @World balance match Formance's world account.
	OpeningIndicator string
	// IncludeZeroBalances creates Blnk balances for accounts whose snapshot
	// balance is zero.
	IncludeZeroBalances bool
	// DryRun computes everything without writing to Blnk or the store.
	DryRun bool
	// Transform, if set, may edit each Blnk request or return keep=false to
	// drop it.
	Transform func(req *blnk.TransactionRequest, tx TxContext) (keep bool, err error)
	// Progress, if set, is called periodically during long phases.
	Progress func(Stats)
	// ProgressEvery is the number of Formance transactions between progress
	// reports. Default 1000.
	ProgressEvery int
}

// Migrator runs migrations. Create one with New.
type Migrator struct {
	src  Source
	dst  Destination
	opts Options
	log  *slog.Logger

	ledgerMu sync.Mutex
	balMu    sync.Mutex
	dryIDs   int
}

// New validates options and returns a Migrator.
func New(src Source, dst Destination, opts Options) (*Migrator, error) {
	if src == nil || dst == nil {
		return nil, errors.New("migrator: source and destination are required")
	}
	if len(opts.Ledgers) == 0 {
		return nil, errors.New("migrator: at least one Formance ledger is required")
	}
	if opts.Mapper == nil {
		m, err := mapping.New(mapping.Config{})
		if err != nil {
			return nil, err
		}
		opts.Mapper = m
	}
	if opts.Store == nil {
		opts.Store = state.NewMemory()
	}
	if opts.DryRun {
		opts.Store = &overlayStore{base: opts.Store, mem: state.NewMemory()}
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if opts.ReferencePrefix == "" {
		opts.ReferencePrefix = "fm"
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = blnk.MaxAtomicBulk
	}
	if opts.BatchSize > blnk.MaxAtomicBulk {
		return nil, fmt.Errorf("migrator: batch size %d exceeds Blnk's atomic limit of %d", opts.BatchSize, blnk.MaxAtomicBulk)
	}
	switch opts.Reverts {
	case "":
		opts.Reverts = RevertReplay
	case RevertReplay, RevertSkip:
	default:
		return nil, fmt.Errorf("migrator: unknown revert policy %q", opts.Reverts)
	}
	switch opts.OnSkippedAccount {
	case "":
		opts.OnSkippedAccount = SkippedAccountError
	case SkippedAccountError, SkippedAccountDrop:
	default:
		return nil, fmt.Errorf("migrator: unknown skipped-account policy %q", opts.OnSkippedAccount)
	}
	if opts.OpeningIndicator == "" {
		t, err := opts.Mapper.Account(opts.Ledgers[0], "world")
		if err != nil || t.Kind != mapping.KindIndicator {
			opts.OpeningIndicator = "@World"
		} else {
			opts.OpeningIndicator = t.Indicator
		}
	}
	if opts.ProgressEvery <= 0 {
		opts.ProgressEvery = 1000
	}
	return &Migrator{src: src, dst: dst, opts: opts, log: opts.Logger}, nil
}

// Stats summarises a run.
type Stats struct {
	Phase           string            `json:"phase"`
	Ledger          string            `json:"ledger,omitempty"`
	FormanceTxs     int64             `json:"formance_transactions"`
	BlnkTxs         int64             `json:"blnk_transactions"`
	AlreadyMigrated int64             `json:"already_migrated"`
	LedgersCreated  int64             `json:"ledgers_created"`
	BalancesCreated int64             `json:"balances_created"`
	Skipped         map[string]int64  `json:"skipped,omitempty"`
	LastTxID        map[string]uint64 `json:"last_tx_id,omitempty"`
	Started         time.Time         `json:"started"`
	Elapsed         time.Duration     `json:"elapsed"`
	DryRun          bool              `json:"dry_run"`
}

func newStats(phase string, dry bool) *Stats {
	return &Stats{Phase: phase, Skipped: map[string]int64{}, LastTxID: map[string]uint64{}, Started: time.Now(), DryRun: dry}
}

func (s *Stats) skip(reason string) { s.Skipped[reason]++ }

func (s *Stats) finish() Stats {
	s.Elapsed = time.Since(s.Started)
	return *s
}

// Phase names used for checkpoints.
const (
	PhaseReplay   = "replay"
	PhaseSnapshot = "snapshot"
)
