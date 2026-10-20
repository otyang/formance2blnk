// Command formance2blnk migrates a Formance ledger into Blnk.
//
//	formance2blnk init > config.json
//	formance2blnk plan      -config config.json
//	formance2blnk migrate   -config config.json
//	formance2blnk reconcile -config config.json -volumes
//
// Run "formance2blnk help" for details.
package main

import (
	"context"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
)

// Set by the release build via -ldflags.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func init() {
	// Binaries installed with "go install ...@vX.Y.Z" carry no ldflags;
	// fall back to the module version and VCS info embedded by the toolchain.
	if version != "dev" {
		return
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		version = v
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			if len(s.Value) > 7 {
				commit = s.Value[:7]
			} else {
				commit = s.Value
			}
		case "vcs.time":
			date = s.Value
		}
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
