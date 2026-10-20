// Package state persists migration progress so that runs can be resumed
// safely: the Blnk IDs created for each ledger and balance, the last
// Formance transaction migrated per ledger, and the transactions that were
// deliberately left out.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"syscall"
	"time"
)

// BalanceKey identifies a migrated balance.
type BalanceKey struct {
	SourceLedger string `json:"source_ledger"` // Formance ledger
	Address      string `json:"address"`       // Formance account address
	Asset        string `json:"asset"`         // Formance asset, e.g. USD/2
}

func (k BalanceKey) String() string { return k.SourceLedger + "|" + k.Address + "|" + k.Asset }

// BalanceRecord is the stored information about a migrated balance.
type BalanceRecord struct {
	BalanceKey
	BalanceID string `json:"balance_id"`
	LedgerID  string `json:"ledger_id"`
}

// Store persists migration state. Implementations must be safe for
// concurrent use.
type Store interface {
	Ledger(name string) (id string, ok bool)
	PutLedger(name, id string) error
	Balance(k BalanceKey) (BalanceRecord, bool)
	PutBalance(r BalanceRecord) error
	Balances() []BalanceRecord
	// Checkpoint returns the last fully migrated transaction ID for a source
	// ledger and phase.
	Checkpoint(sourceLedger, phase string) (uint64, bool)
	SetCheckpoint(sourceLedger, phase string, txID uint64) error
	// MarkSkippedTx records that a source transaction was deliberately not
	// migrated (a reverted transaction under reverts: skip). It must be
	// durable no later than the next SetCheckpoint.
	MarkSkippedTx(sourceLedger string, txID uint64) error
	SkippedTx(sourceLedger string, txID uint64) bool
	// Flush makes all changes durable.
	Flush() error
	Close() error
}

type snapshot struct {
	Version     int                          `json:"version"`
	UpdatedAt   time.Time                    `json:"updated_at"`
	Ledgers     map[string]string            `json:"ledgers"`
	Balances    map[string]BalanceRecord     `json:"balances"`
	Checkpoints map[string]map[string]uint64 `json:"checkpoints"`
	SkippedTxs  map[string]map[uint64]bool   `json:"skipped_txs,omitempty"`
}

func newSnapshot() *snapshot {
	return &snapshot{
		Version:     1,
		Ledgers:     map[string]string{},
		Balances:    map[string]BalanceRecord{},
		Checkpoints: map[string]map[string]uint64{},
	}
}

// Memory is an in-memory Store, useful for dry runs and tests.
type Memory struct {
	mu sync.RWMutex
	s  *snapshot
}

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory { return &Memory{s: newSnapshot()} }

func (m *Memory) Ledger(name string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	id, ok := m.s.Ledgers[name]
	return id, ok
}

func (m *Memory) PutLedger(name, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.s.Ledgers[name] = id
	return nil
}

func (m *Memory) Balance(k BalanceKey) (BalanceRecord, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.s.Balances[k.String()]
	return r, ok
}

func (m *Memory) PutBalance(r BalanceRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.s.Balances[r.BalanceKey.String()] = r
	return nil
}

func (m *Memory) Balances() []BalanceRecord {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]BalanceRecord, 0, len(m.s.Balances))
	for _, r := range m.s.Balances {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].BalanceKey.String() < out[j].BalanceKey.String() })
	return out
}

func (m *Memory) Checkpoint(ledger, phase string) (uint64, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	id, ok := m.s.Checkpoints[ledger][phase]
	return id, ok
}

func (m *Memory) SetCheckpoint(ledger, phase string, id uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.s.Checkpoints[ledger] == nil {
		m.s.Checkpoints[ledger] = map[string]uint64{}
	}
	m.s.Checkpoints[ledger][phase] = id
	return nil
}

func (m *Memory) MarkSkippedTx(ledger string, id uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.s.SkippedTxs == nil {
		m.s.SkippedTxs = map[string]map[uint64]bool{}
	}
	if m.s.SkippedTxs[ledger] == nil {
		m.s.SkippedTxs[ledger] = map[uint64]bool{}
	}
	m.s.SkippedTxs[ledger][id] = true
	return nil
}

func (m *Memory) SkippedTx(ledger string, id uint64) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.s.SkippedTxs[ledger][id]
}

func (m *Memory) Flush() error { return nil }
func (m *Memory) Close() error { return nil }

// File is a Store backed by a JSON file. Writes are atomic (temp file +
// fsync + rename + directory fsync) and a lock file prevents two migrators
// from sharing a state file.
type File struct {
	Memory
	path     string
	lockPath string
	dirty    bool
	wmu      sync.Mutex
}

// ErrLocked is returned when another process holds the state file.
var ErrLocked = errors.New("state: file is locked by another run (remove the .lock file if that run is dead)")

// OpenFile opens (or creates) a file store at path.
func OpenFile(path string) (*File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	lock := path + ".lock"
	lf, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("%w: %s", ErrLocked, lock)
		}
		return nil, err
	}
	fmt.Fprintf(lf, "pid=%d started=%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339))
	lf.Close()

	f := &File{Memory: Memory{s: newSnapshot()}, path: path, lockPath: lock}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		os.Remove(lock)
		return nil, err
	default:
		if err := json.Unmarshal(data, f.s); err != nil {
			os.Remove(lock)
			return nil, fmt.Errorf("state: corrupt file %s: %w", path, err)
		}
		if f.s.Ledgers == nil {
			f.s.Ledgers = map[string]string{}
		}
		if f.s.Balances == nil {
			f.s.Balances = map[string]BalanceRecord{}
		}
		if f.s.Checkpoints == nil {
			f.s.Checkpoints = map[string]map[string]uint64{}
		}
	}
	return f, nil
}

func (f *File) markDirty() {
	f.wmu.Lock()
	f.dirty = true
	f.wmu.Unlock()
}

func (f *File) PutLedger(name, id string) error {
	f.Memory.PutLedger(name, id)
	f.markDirty()
	// Created IDs are expensive to lose, so persist immediately.
	return f.Flush()
}

func (f *File) PutBalance(r BalanceRecord) error {
	f.Memory.PutBalance(r)
	f.markDirty()
	return f.Flush()
}

func (f *File) SetCheckpoint(ledger, phase string, id uint64) error {
	f.Memory.SetCheckpoint(ledger, phase, id)
	f.markDirty()
	return f.Flush()
}

// MarkSkippedTx is written with the next checkpoint, which always follows
// it, rather than costing a file write of its own.
func (f *File) MarkSkippedTx(ledger string, id uint64) error {
	f.Memory.MarkSkippedTx(ledger, id)
	f.markDirty()
	return nil
}

// Flush writes the state atomically if it changed.
func (f *File) Flush() error {
	f.wmu.Lock()
	defer f.wmu.Unlock()
	if !f.dirty {
		return nil
	}
	f.mu.RLock()
	f.s.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(f.s, "", "  ")
	f.mu.RUnlock()
	if err != nil {
		return err
	}
	tmp := f.path + ".tmp"
	fh, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := fh.Write(data); err != nil {
		fh.Close()
		return err
	}
	if err := fh.Sync(); err != nil {
		fh.Close()
		return err
	}
	if err := fh.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, f.path); err != nil {
		return err
	}
	// The rename is only durable once the directory entry is on disk.
	if err := syncDir(filepath.Dir(f.path)); err != nil {
		return err
	}
	f.dirty = false
	return nil
}

func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil // directories cannot be fsynced there; rename is durable
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	// Some filesystems do not support fsync on directories.
	if err := d.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) {
		return err
	}
	return nil
}

// Close flushes and releases the lock.
func (f *File) Close() error {
	err := f.Flush()
	if rerr := os.Remove(f.lockPath); rerr != nil && !errors.Is(rerr, os.ErrNotExist) && err == nil {
		err = rerr
	}
	return err
}
