package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/otyang/formance2blnk/config"
	"github.com/otyang/formance2blnk/migrator"
)

// Exit codes.
const (
	exitOK       = 0
	exitError    = 1
	exitMismatch = 2
	exitUsage    = 64
)

const usage = `formance2blnk — migrate a Formance ledger into Blnk

Usage:
  formance2blnk <command> [flags]

Commands:
  init        Print an example configuration file
  validate    Check the configuration file
  plan        Dry run: show what a migration would create, write nothing
  migrate     Run the migration (resumable; safe to re-run)
  reconcile   Compare Formance and Blnk balances
  version     Print version information

Common flags:
  -config path       Configuration file (default "formance2blnk.json")
  -log-level level   debug, info, warn, error (default "info")
  -log-format fmt    text or json (default "text")

Run "formance2blnk <command> -h" for command flags.
Exit codes: 0 ok, 1 error, 2 reconciliation mismatch, 64 usage error.
`

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return exitOK
	case "version", "-version", "--version":
		fmt.Fprintf(stdout, "formance2blnk %s (commit %s, built %s)\n", version, commit, date)
		return exitOK
	case "init":
		fmt.Fprint(stdout, config.Example)
		return exitOK
	case "validate", "plan", "migrate", "reconcile":
		return runCommand(ctx, cmd, rest, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", cmd, usage)
		return exitUsage
	}
}

type common struct {
	configPath string
	logLevel   string
	logFormat  string
	stateFile  string
}

func runCommand(ctx context.Context, cmd string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var c common
	fs.StringVar(&c.configPath, "config", "formance2blnk.json", "configuration file")
	fs.StringVar(&c.logLevel, "log-level", "info", "log level: debug, info, warn, error")
	fs.StringVar(&c.logFormat, "log-format", "text", "log format: text or json")
	fs.StringVar(&c.stateFile, "state", "", "state file (overrides config)")

	var (
		mode    = fs.String("mode", "", "override migration.mode: replay, snapshot or hybrid")
		cutoff  = fs.String("cutoff", "", "override migration.cutoff (RFC 3339) for hybrid mode")
		jsonOut = fs.Bool("json", false, "print the summary as JSON")
		volumes = fs.Bool("volumes", false, "reconcile: also compare credit/debit volumes (after a full replay)")
		skipInd = fs.Bool("skip-indicators", false, "reconcile: ignore @indicator balances")
		format  = fs.String("format", "text", "reconcile: output format text, json or csv")
		out     = fs.String("out", "", "reconcile: write the report to this file instead of stdout")
		workers = fs.Int("workers", 8, "reconcile: concurrent balance lookups")
	)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}

	logger, err := newLogger(c.logLevel, c.logFormat, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}

	cfg, err := config.Load(c.configPath)
	if err == nil && (*mode != "" || *cutoff != "" || c.stateFile != "") {
		if *mode != "" {
			cfg.Migration.Mode = config.Mode(*mode)
		}
		if *cutoff != "" {
			cfg.Migration.Cutoff = *cutoff
		}
		if c.stateFile != "" {
			cfg.StateFile = c.stateFile
		}
		err = cfg.Validate()
	}
	if err != nil {
		fmt.Fprintf(stderr, "invalid configuration %s:\n  %v\n", c.configPath, strings.ReplaceAll(err.Error(), "\n", "\n  "))
		return exitError
	}
	if cmd == "validate" {
		fmt.Fprintf(stdout, "configuration OK\n%s\n", cfg)
		return exitOK
	}

	src, err := cfg.FormanceClient()
	if err != nil {
		return fail(stderr, err)
	}
	dst, err := cfg.BlnkClient()
	if err != nil {
		return fail(stderr, err)
	}
	opts, err := cfg.MigratorOptions(logger)
	if err != nil {
		return fail(stderr, err)
	}
	store, err := cfg.OpenState()
	if err != nil {
		return fail(stderr, err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			logger.Error("closing state file", "err", err)
		}
	}()
	opts.Store = store
	opts.DryRun = cmd == "plan"

	m, err := migrator.New(src, dst, opts)
	if err != nil {
		return fail(stderr, err)
	}

	switch cmd {
	case "plan", "migrate":
		var st migrator.Stats
		switch cfg.Migration.Mode {
		case config.ModeReplay:
			st, err = m.Replay(ctx)
		case config.ModeSnapshot:
			st, err = m.Snapshot(ctx)
		case config.ModeHybrid:
			t, _ := cfg.CutoffTime()
			st, err = m.Hybrid(ctx, t)
		}
		printStats(stdout, st, *jsonOut)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				fmt.Fprintln(stderr, "interrupted: progress is saved, re-run the same command to resume")
				return exitError
			}
			return fail(stderr, err)
		}
		if cmd == "plan" {
			fmt.Fprintln(stdout, "\nplan only: nothing was written to Blnk or the state file")
		}
		return exitOK

	case "reconcile":
		rep, err := m.Reconcile(ctx, migrator.ReconcileOptions{CheckVolumes: *volumes, SkipIndicators: *skipInd, Workers: *workers})
		if err != nil {
			return fail(stderr, err)
		}
		w := stdout
		if *out != "" {
			f, err := os.Create(*out)
			if err != nil {
				return fail(stderr, err)
			}
			defer f.Close()
			w = f
		}
		if err := writeReport(w, rep, *format); err != nil {
			return fail(stderr, err)
		}
		if *out != "" {
			fmt.Fprintf(stdout, "checked %d, matched %d, mismatches %d (report: %s)\n", rep.Checked, rep.Matched, len(rep.Mismatches), *out)
		}
		if !rep.OK() {
			return exitMismatch
		}
		return exitOK
	}
	return exitOK
}

func fail(w io.Writer, err error) int {
	fmt.Fprintf(w, "error: %v\n", err)
	return exitError
}

func newLogger(level, format string, w io.Writer) (*slog.Logger, error) {
	var lv slog.Level
	if err := lv.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("invalid -log-level %q", level)
	}
	ho := &slog.HandlerOptions{Level: lv}
	switch format {
	case "text":
		return slog.New(slog.NewTextHandler(w, ho)), nil
	case "json":
		return slog.New(slog.NewJSONHandler(w, ho)), nil
	}
	return nil, fmt.Errorf("invalid -log-format %q", format)
}

func printStats(w io.Writer, st migrator.Stats, asJSON bool) {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(st)
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	title := st.Phase
	if st.DryRun {
		title += " (plan)"
	}
	fmt.Fprintf(tw, "\n%s summary\n", title)
	fmt.Fprintf(tw, "  Formance transactions read\t%d\n", st.FormanceTxs)
	fmt.Fprintf(tw, "  Blnk transactions written\t%d\n", st.BlnkTxs)
	fmt.Fprintf(tw, "  Already migrated (skipped)\t%d\n", st.AlreadyMigrated)
	fmt.Fprintf(tw, "  Blnk ledgers created\t%d\n", st.LedgersCreated)
	fmt.Fprintf(tw, "  Blnk balances created\t%d\n", st.BalancesCreated)
	keys := make([]string, 0, len(st.Skipped))
	for k := range st.Skipped {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(tw, "  Skipped: %s\t%d\n", k, st.Skipped[k])
	}
	for l, id := range st.LastTxID {
		fmt.Fprintf(tw, "  Checkpoint %s\ttx %d\n", l, id)
	}
	fmt.Fprintf(tw, "  Elapsed\t%s\n", st.Elapsed.Round(time.Millisecond))
	tw.Flush()
}

func writeReport(w io.Writer, rep migrator.Report, format string) error {
	switch format {
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	case "csv":
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"target", "accounts", "field", "expected", "actual", "detail"})
		for _, m := range rep.Mismatches {
			_ = cw.Write([]string{m.Target, m.Accounts, m.Field, m.Expected, m.Actual, m.Detail})
		}
		cw.Flush()
		return cw.Error()
	case "text":
		status := "OK"
		if !rep.OK() {
			status = "MISMATCH"
		}
		fmt.Fprintf(w, "reconciliation %s: checked %d, matched %d, skipped %d, mismatches %d\n",
			status, rep.Checked, rep.Matched, rep.Skipped, len(rep.Mismatches))
		if rep.OK() {
			return nil
		}
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "\nFIELD\tEXPECTED\tACTUAL\tTARGET\tACCOUNTS")
		for _, m := range rep.Mismatches {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", m.Field, m.Expected, m.Actual, m.Target, m.Accounts)
		}
		return tw.Flush()
	}
	return fmt.Errorf("unknown -format %q", format)
}
