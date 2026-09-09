# ktui

A terminal UI for managing `kubectl` contexts: list, switch, rename, set namespace,
and delete — with the cluster/user orphan cleanup that `kubectl config delete-context`
leaves behind.

```
ktui 3 contexts  current: prod-eu
~/.kube/config  (+1 more via KUBECONFIG)

      CONTEXT           NAMESPACE   STATUS        LATENCY
▸     prod-eu *         payments    live          42ms
      staging           default     auth expired  118ms
      old-minikube      default     unreachable   5.0s

╭──────────────────────────────────────────────────────────╮
│  server    https://ABC123.gr7.eu-west-1.eks.amazonaws.com │
│  cluster   prod-eu   user prod-eu                         │
│  auth      exec:aws   file ~/.kube/config                 │
╰──────────────────────────────────────────────────────────╯
enter switch · n ns · r rename · d delete · space select · / filter · ? help
```

## Install

```bash
brew tap razvanbalsan/tap
brew trust razvanbalsan/tap   # recent Homebrew: third-party taps start untrusted
brew install ktui
```

Or without Homebrew:

```bash
go install github.com/razvanbalsan/ktui@latest   # needs Go 1.22+
```

Prebuilt binaries for macOS and Linux (arm64 and amd64) are attached to every
[release](https://github.com/razvanbalsan/ktui/releases). The binary has no
runtime dependencies — it talks to the kubeconfig and the API servers directly,
not through `kubectl`.

## Keys

| Key | Action |
|---|---|
| `j`/`k`, `↑`/`↓` | move |
| `g` / `G` | first / last |
| `ctrl+d` / `ctrl+u` | half page |
| `enter` | switch current context |
| `n` | namespaces for this context; `enter` sets the default |
| `r` | rename context |
| `space` | toggle selection |
| `a` | select / deselect everything visible |
| `d` | delete selection (or the highlighted row) |
| `p` | re-run reachability probes |
| `/` | filter by name, cluster or server |
| `esc` | clear selection, then clear filter |
| `?` | help |
| `q` | quit — asks to confirm first |
| `ctrl+c` | quit immediately, no prompt |

## What it does that `kubectl config` doesn't

**Orphan cleanup.** `kubectl config delete-context foo` removes the context
stanza and leaves the `cluster` and `user` entries it referenced sitting in
`~/.kube/config` forever. This deletes them too — but only when no surviving
context still points at them. The confirmation screen shows both lists before
you commit:

```
  no longer referenced, will also be removed:
  − cluster lonely-cluster
  − user    lonely-user

  kept, still used by other contexts:
  · cluster shared-cluster
```

**Correct multi-file writes.** With a merged `KUBECONFIG` chain, each change is
written back to the file that actually defined the entry, via client-go's
`ModifyConfig`. A context defined in the second file is deleted from the second
file. The detail pane shows which file each context came from.

**Backups.** Every write is preceded by a timestamped snapshot of the entire
kubeconfig chain under `~/.kube/ktui-backups/<timestamp>/`, pruned to the
last 10. Disable with `--no-backup`.

## The status column

Each context is probed with one `list namespaces` call capped at a single item.
The response code carries the signal:

| Status | Meaning |
|---|---|
| `live` | API server answered, credentials accepted (a 403 counts — RBAC declining the verb still means auth worked) |
| `auth expired` | reachable, credentials rejected (401) — time to re-login |
| `cred error` | the credential plugin failed before any request went out (e.g. `aws` not on PATH) |
| `tls error` | reachable, server certificate not trusted |
| `unreachable` | DNS failure, connection refused, or timeout |

Probes run concurrently, capped at 8 in flight, with a 5s per-context timeout
(`--probe-timeout`).

**Exec credential plugins are pinned to `InteractiveMode: Never`**, so a plugin
can't grab the terminal out from under the TUI. The practical consequence: a
context whose plugin needs an interactive login shows `cred error` rather than
prompting. Run the login command yourself and press `p` to re-probe.

Note that probing *does* execute credential plugins (`aws eks get-token`,
`kubelogin`, …) for every context at startup. On a large kubeconfig that is a
burst of subprocesses and, for some plugins, a burst of token requests. Start
with `--no-probe` and press `p` on demand if that's not what you want.

## Flags

```
--no-probe              skip reachability probes at startup (press p to run them)
--probe-timeout 5s      per-context probe timeout
--no-backup             do not snapshot the kubeconfig before writes
--backup-dir PATH       where snapshots are kept (default ~/.kube/ktui-backups)
--keep-backups 10       snapshots to retain (0 = keep all)
--exit-on-switch        quit and print the name after switching, kubectx-style
--list                  print contexts to stdout and exit (no TUI)
--version
```

`--list` is the scriptable path:

```bash
ktui --list --no-probe | awk '$1=="*"{print $2}'   # current context
ktui --list | grep unreachable                     # dead contexts worth pruning
```

## Caveats

- **Rename across a merged chain.** A rename is a delete plus an add. client-go
  writes new entries to the *destination* file, so renaming a context that lives
  in a non-primary `KUBECONFIG` file may relocate it to the primary one. Single-file
  setups (the common case) are unaffected. [VERIFY: exact relocation behaviour
  against your own KUBECONFIG layout before relying on it.]
- Deleting the current context leaves `current-context` empty rather than guessing
  a replacement. The confirmation warns you first.
- `--list` with probing enabled runs the same credential plugins the TUI does.

## Tests

```bash
make test
```

33 tests covering orphan resolution, multi-file write targeting, backup and prune
behaviour, and the TUI itself — the model is driven through `Update`/`View`
directly, so no terminal or cluster is needed.

## Releasing

Tag and push; the release workflow cross-compiles the four platform archives,
attaches them with checksums, and prints the formula block to paste into the tap.

```bash
make release VERSION=0.2.0     # tags v0.2.0 and pushes
```

## License

GPL-3.0-or-later. See [LICENSE](LICENSE).

This program is distributed in the hope that it will be useful, but WITHOUT ANY
WARRANTY; without even the implied warranty of MERCHANTABILITY or FITNESS FOR A
PARTICULAR PURPOSE.
