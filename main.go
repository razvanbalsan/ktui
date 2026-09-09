// ktui — a terminal UI for managing kubectl contexts.
// Copyright (C) 2026 Razvan Balsan
//
// This program is free software: you can redistribute it and/or modify it under
// the terms of the GNU General Public License as published by the Free Software
// Foundation, either version 3 of the License, or (at your option) any later
// version. This program is distributed WITHOUT ANY WARRANTY; see the GNU
// General Public License in the LICENSE file for details.

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

var version = "0.1.0"

type Options struct {
	Probe        bool
	ProbeTimeout time.Duration
	ExitOnSwitch bool
}

func defaultBackupDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "ktui-backups")
	}
	return filepath.Join(home, ".kube", "ktui-backups")
}

func main() {
	var (
		noProbe      = flag.Bool("no-probe", false, "skip reachability probes at startup (press p to run them)")
		probeTimeout = flag.Duration("probe-timeout", 5*time.Second, "per-context timeout for reachability probes")
		noBackup     = flag.Bool("no-backup", false, "do not snapshot the kubeconfig before writes")
		backupDir    = flag.String("backup-dir", defaultBackupDir(), "where kubeconfig snapshots are kept")
		keepBackups  = flag.Int("keep-backups", 10, "number of snapshots to retain (0 = keep all)")
		exitOnSwitch = flag.Bool("exit-on-switch", false, "quit and print the name after switching context")
		listOnly     = flag.Bool("list", false, "print contexts to stdout and exit (no TUI)")
		showVersion  = flag.Bool("version", false, "print version and exit")
	)
	flag.Usage = usage
	flag.Parse()

	if *showVersion {
		fmt.Println("ktui", version)
		return
	}

	store, err := NewStore(*backupDir, *keepBackups, !*noBackup)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	if len(store.Contexts()) == 0 {
		fmt.Fprintln(os.Stderr, "no contexts found in",
			strings.Join(store.pathOptions.GetLoadingPrecedence(), ", "))
		os.Exit(1)
	}

	opts := Options{
		Probe:        !*noProbe,
		ProbeTimeout: *probeTimeout,
		ExitOnSwitch: *exitOnSwitch,
	}

	if *listOnly {
		listContexts(store, opts)
		return
	}

	p := tea.NewProgram(newModel(store, opts), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// listContexts is the scriptable path: no TUI, one line per context.
func listContexts(store *Store, opts Options) {
	ctxs := store.Contexts()
	results := make([]ProbeResult, len(ctxs))

	if opts.Probe {
		var wg sync.WaitGroup
		for i, c := range ctxs {
			wg.Add(1)
			go func(i int, name string) {
				defer wg.Done()
				probeSem <- struct{}{}
				defer func() { <-probeSem }()
				results[i] = Probe(context.Background(), name, opts.ProbeTimeout)
			}(i, c.Name)
		}
		wg.Wait()
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "CURRENT\tNAME\tNAMESPACE\tSTATUS\tSERVER")
	for i, c := range ctxs {
		cur := ""
		if c.IsCurrent {
			cur = "*"
		}
		status := "-"
		if opts.Probe {
			status = results[i].State.Label()
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", cur, c.Name, c.Namespace, status, c.Server)
	}
	w.Flush()
}

func usage() {
	fmt.Fprintf(os.Stderr, `ktui %s — a terminal UI for managing kubectl contexts

Usage:
  ktui [flags]

Keys:
  enter  switch context      n  namespaces      r  rename
  d      delete              space  select      /  filter
  p      re-probe            ?  help            q  quit

Deleting a context also removes the cluster and user entries it referenced,
when no surviving context still points at them. Every write is preceded by a
timestamped snapshot of the whole kubeconfig chain.

Flags:
`, version)
	flag.PrintDefaults()
}
