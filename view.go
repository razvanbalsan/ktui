package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

var (
	colSubtle = lipgloss.AdaptiveColor{Light: "#6C7086", Dark: "#8A8FA3"}
	colAccent = lipgloss.AdaptiveColor{Light: "#1F6FEB", Dark: "#6CB6FF"}
	colOK     = lipgloss.AdaptiveColor{Light: "#1A7F37", Dark: "#57C97E"}
	colWarn   = lipgloss.AdaptiveColor{Light: "#9A6700", Dark: "#E3B341"}
	colErr    = lipgloss.AdaptiveColor{Light: "#CF222E", Dark: "#FF7B72"}
	colText   = lipgloss.AdaptiveColor{Light: "#1C2128", Dark: "#E6EDF3"}

	stTitle    = lipgloss.NewStyle().Bold(true).Foreground(colText)
	stSubtle   = lipgloss.NewStyle().Foreground(colSubtle)
	stAccent   = lipgloss.NewStyle().Foreground(colAccent)
	stOK       = lipgloss.NewStyle().Foreground(colOK)
	stWarn     = lipgloss.NewStyle().Foreground(colWarn)
	stErr      = lipgloss.NewStyle().Foreground(colErr)
	stCursor   = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
	stKey      = lipgloss.NewStyle().Bold(true).Foreground(colText)
	stBoxTitle = lipgloss.NewStyle().Bold(true).Foreground(colText)
	stBox      = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colSubtle).
			Padding(0, 2)
)

func stateStyle(s ProbeState) lipgloss.Style {
	switch s {
	case ProbeLive:
		return stOK
	case ProbeAuthExpired, ProbeTLSError, ProbeCredError:
		return stWarn
	case ProbeUnreachable:
		return stErr
	}
	return stSubtle
}

func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func pad(s string, n int) string {
	w := lipgloss.Width(s)
	if w >= n {
		return s
	}
	return s + strings.Repeat(" ", n-w)
}

func shortPath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

func (m model) listHeight() int {
	// header(2) + column heading(1) + detail(5) + status(2)
	h := m.height - 10
	if h < 3 {
		h = 3
	}
	return h
}

func (m model) View() string {
	if m.quitting {
		return ""
	}
	switch m.mode {
	case viewHelp:
		return m.viewHelp()
	case viewConfirmDelete:
		return m.viewConfirm()
	case viewConfirmQuit:
		return m.viewConfirmQuit()
	case viewNamespaces:
		return m.viewNamespaces()
	case viewRename:
		return m.viewListWith(m.renamePrompt())
	}
	return m.viewListWith("")
}

func (m model) header() string {
	cur := m.store.CurrentContext()
	if cur == "" {
		cur = "(none)"
	}
	left := stTitle.Render("ktui")
	meta := stSubtle.Render(fmt.Sprintf(" %d contexts", len(m.contexts)))
	right := stSubtle.Render("current: ") + stAccent.Render(cur)

	files := m.store.Files()
	var fileLine string
	switch len(files) {
	case 0:
		fileLine = stErr.Render("no kubeconfig found")
	case 1:
		fileLine = stSubtle.Render(shortPath(files[0]))
	default:
		fileLine = stSubtle.Render(fmt.Sprintf("%s  (+%d more via KUBECONFIG)",
			shortPath(files[0]), len(files)-1))
	}
	return left + meta + "  " + right + "\n" + fileLine
}

// columns computes the width of each list column for the current terminal size.
func (m model) columns() (name, ns, status, lat int) {
	ns, status, lat = 18, 13, 7
	fixed := 2 + 4 + ns + status + lat + 4 // marker + checkbox + gaps
	name = m.width - fixed
	if name < 16 {
		// Very narrow terminal: drop latency, then squeeze the namespace column.
		lat = 0
		name = m.width - (2 + 4 + ns + status + 3)
	}
	if name < 12 {
		ns = 10
		name = m.width - (2 + 4 + ns + status + 3)
	}
	if name < 8 {
		name = 8
	}
	return
}

func (m model) viewListWith(overlay string) string {
	var b strings.Builder
	b.WriteString(m.header())
	b.WriteString("\n\n")

	nameW, nsW, stW, latW := m.columns()

	// Shrink the name column to the widest visible name so short context names
	// do not leave a canyon between the columns; long ARNs still get the space.
	widest := 0
	for _, i := range m.view {
		if w := lipgloss.Width(m.contexts[i].Name) + 2; w > widest {
			widest = w
		}
	}
	if widest > 0 && widest < nameW {
		nameW = widest
		if nameW < 16 {
			nameW = 16
		}
	}

	head := "  " + pad("", 4) + pad("CONTEXT", nameW+1) + pad("NAMESPACE", nsW+1) + pad("STATUS", stW+1)
	if latW > 0 {
		head += "LATENCY"
	}
	b.WriteString(stSubtle.Render(truncate(head, m.width)))
	b.WriteString("\n")

	h := m.listHeight()
	offset := m.offset
	if m.cursor < offset {
		offset = m.cursor
	}
	if m.cursor >= offset+h {
		offset = m.cursor - h + 1
	}
	if offset < 0 {
		offset = 0
	}

	if len(m.view) == 0 {
		b.WriteString(stSubtle.Render("  no contexts match"))
		b.WriteString("\n")
		for i := 1; i < h; i++ {
			b.WriteString("\n")
		}
	} else {
		end := offset + h
		if end > len(m.view) {
			end = len(m.view)
		}
		for i := offset; i < end; i++ {
			b.WriteString(m.renderRow(i, i == m.cursor, nameW, nsW, stW, latW))
			b.WriteString("\n")
		}
		for i := end - offset; i < h; i++ {
			b.WriteString("\n")
		}
	}

	b.WriteString("\n")
	b.WriteString(m.detail())
	b.WriteString("\n")

	if overlay != "" {
		b.WriteString(overlay)
	} else {
		b.WriteString(m.statusBar())
	}
	return b.String()
}

func (m model) renderRow(i int, isCursor bool, nameW, nsW, stW, latW int) string {
	c := m.contexts[m.view[i]]

	marker := "  "
	if isCursor {
		marker = stCursor.Render("▸ ")
	}

	box := "    "
	if len(m.selected) > 0 {
		if m.selected[c.Name] {
			box = stAccent.Render("[x] ")
		} else {
			box = stSubtle.Render("[ ] ")
		}
	}

	name := truncate(c.Name, nameW)
	nameStyled := pad(name, nameW+1)
	switch {
	case c.IsCurrent && isCursor:
		nameStyled = stCursor.Render(pad(name+" *", nameW+1))
	case c.IsCurrent:
		nameStyled = lipgloss.NewStyle().Bold(true).Foreground(colOK).Render(pad(name+" *", nameW+1))
	case isCursor:
		nameStyled = stCursor.Render(nameStyled)
	default:
		nameStyled = lipgloss.NewStyle().Foreground(colText).Render(nameStyled)
	}

	nsCell := stSubtle.Render(pad(truncate(c.Namespace, nsW), nsW+1))

	pr, probed := m.probes[c.Name]
	label := ""
	if probed {
		label = pr.State.Label()
	} else if m.opts.Probe {
		label = "—"
	}
	stCell := stateStyle(pr.State).Render(pad(truncate(label, stW), stW+1))

	latCell := ""
	if latW > 0 && probed && pr.State != ProbePending {
		latCell = stSubtle.Render(formatLatency(pr.Latency))
	}

	return truncate(marker+box+nameStyled+nsCell+stCell+latCell, m.width+40)
}

func (m model) detail() string {
	c, ok := m.current()
	if !ok {
		return stBox.Width(m.width - 4).Render(stSubtle.Render("nothing selected"))
	}
	label := func(s string) string { return stSubtle.Render(pad(s, 10)) }

	lines := []string{
		label("server") + truncate(orDash(c.Server), m.width-18),
		label("cluster") + truncate(orDash(c.Cluster), m.width-18) +
			stSubtle.Render("   user ") + truncate(orDash(c.AuthInfo), 24),
		label("auth") + truncate(c.AuthKind, 28) +
			stSubtle.Render("   file ") + truncate(shortPath(c.SourceFile), m.width-50),
	}
	if pr, ok := m.probes[c.Name]; ok && pr.Detail != "" && pr.State != ProbeLive {
		lines = append(lines, label("detail")+stateStyle(pr.State).Render(truncate(pr.Detail, m.width-18)))
	}
	return stBox.Width(m.width - 4).Render(strings.Join(lines, "\n"))
}

func formatLatency(d time.Duration) string {
	if d >= time.Second {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dms", d.Milliseconds())
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func (m model) statusBar() string {
	if m.filtering {
		return m.filter.View()
	}
	if m.status != "" {
		if m.statusErr {
			return stErr.Render("✗ " + truncate(m.status, m.width-2))
		}
		return stOK.Render("✓ " + truncate(m.status, m.width-2))
	}

	sel := ""
	if n := len(m.selected); n > 0 {
		sel = stAccent.Render(fmt.Sprintf("%d selected  ", n))
	}
	if f := m.filter.Value(); f != "" {
		sel += stSubtle.Render(fmt.Sprintf("filter:%q  ", f))
	}

	return sel + m.keyHints(lipgloss.Width(sel), [][2]string{
		{"enter", "switch"},
		{"n", "ns"},
		{"r", "rename"},
		{"d", "delete"},
		{"space", "select"},
		{"/", "filter"},
		{"q", "quit"},
		{"?", "help"},
	})
}

// keyHints joins as many hints as fit on one line, dropping from the right, so
// a narrow terminal loses the last hint instead of wrapping the whole bar.
func (m model) keyHints(used int, hints [][2]string) string {
	const sep = " · "
	var parts []string
	for i, h := range hints {
		w := lipgloss.Width(h[0]) + 1 + lipgloss.Width(h[1])
		if i > 0 {
			w += lipgloss.Width(sep)
		}
		if used+w > m.width {
			break
		}
		used += w
		parts = append(parts, stKey.Render(h[0])+stSubtle.Render(" "+h[1]))
	}
	return strings.Join(parts, stSubtle.Render(sep))
}

func (m model) renamePrompt() string {
	return stAccent.Render("rename ") + stSubtle.Render(m.renameFrom+" →") + "\n" +
		m.rename.View() + "  " + stSubtle.Render("enter confirm · esc cancel")
}

func (m model) viewConfirm() string {
	var b strings.Builder
	b.WriteString(stBoxTitle.Render("Delete " + plural(len(m.plan.Contexts), "context", "contexts")))
	b.WriteString("\n\n")

	for _, n := range m.plan.Contexts {
		b.WriteString("  " + stErr.Render("− ") + n + "\n")
	}

	if len(m.plan.OrphanClusters) > 0 || len(m.plan.OrphanUsers) > 0 {
		b.WriteString("\n" + stSubtle.Render("  no longer referenced, will also be removed:") + "\n")
		for _, c := range m.plan.OrphanClusters {
			b.WriteString("  " + stErr.Render("− ") + stSubtle.Render("cluster ") + c + "\n")
		}
		for _, u := range m.plan.OrphanUsers {
			b.WriteString("  " + stErr.Render("− ") + stSubtle.Render("user    ") + u + "\n")
		}
	}
	if len(m.plan.KeptClusters) > 0 || len(m.plan.KeptUsers) > 0 {
		b.WriteString("\n" + stSubtle.Render("  kept, still used by other contexts:") + "\n")
		for _, c := range m.plan.KeptClusters {
			b.WriteString("  " + stSubtle.Render("· cluster "+c) + "\n")
		}
		for _, u := range m.plan.KeptUsers {
			b.WriteString("  " + stSubtle.Render("· user    "+u) + "\n")
		}
	}

	if m.plan.ClearsCurrent {
		b.WriteString("\n  " + stWarn.Render("⚠ this removes the current context; kubectl will have none set") + "\n")
	}

	b.WriteString("\n")
	if m.store.Backups {
		b.WriteString(stSubtle.Render("  backup → "+shortPath(m.store.BackupDir)+"/<timestamp>/") + "\n")
	} else {
		b.WriteString(stWarn.Render("  backups disabled (--no-backup)") + "\n")
	}
	for _, f := range m.plan.Files {
		b.WriteString(stSubtle.Render("  rewrites "+shortPath(f)) + "\n")
	}

	b.WriteString("\n  " + stKey.Render("y") + stSubtle.Render(" confirm   ") +
		stKey.Render("n") + stSubtle.Render("/") + stKey.Render("esc") + stSubtle.Render(" cancel"))

	return stBox.Width(m.width - 4).Render(b.String())
}

// fit returns the first phrasing that fits in w cells, so a narrow terminal gets
// a shorter sentence rather than a wrapped or elided one.
func fit(w int, variants ...string) string {
	for _, v := range variants {
		if lipgloss.Width(v) <= w {
			return v
		}
	}
	return truncate(variants[len(variants)-1], w)
}

func (m model) viewConfirmQuit() string {
	var b strings.Builder
	b.WriteString(stBoxTitle.Render("Quit ktui?"))
	b.WriteString("\n\n")

	// The box keeps two columns of padding, so this is what a line has room for.
	avail := m.width - 12
	if avail < 8 {
		avail = 8
	}

	cur := m.store.CurrentContext()
	if cur == "" {
		cur = "(none)"
	}
	// "kubectl keeps X as its current context." needs 40 cells around the name;
	// when the name does not leave that much, give it a line of its own.
	if avail >= 40+lipgloss.Width(cur) {
		b.WriteString("  " + stSubtle.Render("kubectl keeps ") + stAccent.Render(cur) +
			stSubtle.Render(" as its current context.") + "\n")
	} else {
		b.WriteString("  " + stSubtle.Render(fit(avail, "current context", "current")) + "\n")
		b.WriteString("  " + stAccent.Render(truncate(cur, avail)) + "\n")
	}

	if n := len(m.selected); n > 0 {
		b.WriteString("  " + stSubtle.Render(fit(avail,
			fmt.Sprintf("%s will be forgotten.", plural(n, "selected context", "selected contexts")),
			fmt.Sprintf("%d selected, forgotten on quit.", n),
			fmt.Sprintf("%d selected.", n))) + "\n")
	}

	b.WriteString("\n  " + stSubtle.Render(fit(avail,
		"Nothing is pending: switches, renames and deletes are written as you make them.",
		"Nothing is pending; every change is already on disk.",
		"Nothing is pending; changes are already saved.",
		"Nothing is pending.")) + "\n")

	if avail >= 27 {
		b.WriteString("\n  " + stKey.Render("y") + stSubtle.Render("/") + stKey.Render("q") +
			stSubtle.Render("/") + stKey.Render("enter") + stSubtle.Render(" quit   ") +
			stKey.Render("n") + stSubtle.Render("/") + stKey.Render("esc") + stSubtle.Render(" stay"))
	} else {
		b.WriteString("\n  " + stKey.Render("y") + stSubtle.Render(" quit  ") +
			stKey.Render("n") + stSubtle.Render(" stay"))
	}

	return stBox.Width(m.width - 4).Render(b.String())
}

func (m model) viewNamespaces() string {
	var b strings.Builder
	b.WriteString(stTitle.Render("namespaces") + stSubtle.Render("  in ") + stAccent.Render(m.nsContext))
	b.WriteString("\n\n")

	if m.nsLoading {
		b.WriteString(stSubtle.Render("  loading…"))
		return b.String()
	}

	cur := ""
	if c, ok := m.contextByName(m.nsContext); ok {
		cur = c.Namespace
	}

	h := m.height - 8
	if h < 3 {
		h = 3
	}
	offset := 0
	if m.nsCursor >= h {
		offset = m.nsCursor - h + 1
	}
	end := offset + h
	if end > len(m.nsView) {
		end = len(m.nsView)
	}

	if len(m.nsView) == 0 {
		b.WriteString(stSubtle.Render("  no namespaces match") + "\n")
	}
	for i := offset; i < end; i++ {
		ns := m.nsItems[m.nsView[i]]
		line := "  " + ns
		if ns == cur {
			line += stSubtle.Render("  (current)")
		}
		if i == m.nsCursor {
			b.WriteString(stCursor.Render("▸ "+ns) + func() string {
				if ns == cur {
					return stSubtle.Render("  (current)")
				}
				return ""
			}() + "\n")
		} else {
			b.WriteString(line + "\n")
		}
	}

	b.WriteString("\n")
	if m.nsFiltering {
		b.WriteString(m.nsFilter.View())
	} else {
		b.WriteString(stKey.Render("enter") + stSubtle.Render(" set · ") +
			stKey.Render("/") + stSubtle.Render(" filter · ") +
			stKey.Render("esc") + stSubtle.Render(" back"))
	}
	return b.String()
}

func (m model) viewHelp() string {
	rows := [][2]string{
		{"j / k, ↑ / ↓", "move"},
		{"g / G", "first / last"},
		{"ctrl+d / ctrl+u", "half page down / up"},
		{"enter", "switch current context to the highlighted one"},
		{"n", "list namespaces for this context, enter to set the default"},
		{"r", "rename this context"},
		{"space", "toggle selection (delete then applies to the selection)"},
		{"a", "select / deselect everything visible"},
		{"d", "delete selection, or the highlighted context"},
		{"p", "re-run reachability probes"},
		{"/", "filter by name, cluster or server"},
		{"esc", "clear selection, then clear filter"},
		{"?", "this help"},
		{"q", "quit, after a confirmation"},
		{"ctrl+c", "quit immediately"},
	}

	var b strings.Builder
	b.WriteString(stBoxTitle.Render("ktui — keys") + "\n\n")
	for _, r := range rows {
		b.WriteString("  " + stKey.Render(pad(r[0], 18)) + stSubtle.Render(r[1]) + "\n")
	}

	b.WriteString("\n" + stBoxTitle.Render("status column") + "\n\n")
	states := [][2]string{
		{"live", "API server answered and accepted the credentials"},
		{"auth expired", "reachable, credentials rejected (401) — re-login"},
		{"cred error", "credential plugin failed before any request went out"},
		{"tls error", "reachable, server certificate not trusted"},
		{"unreachable", "DNS failure, connection refused, or timeout"},
	}
	for _, s := range states {
		style := stSubtle
		switch s[0] {
		case "live":
			style = stOK
		case "unreachable":
			style = stErr
		case "auth expired", "cred error", "tls error":
			style = stWarn
		}
		b.WriteString("  " + style.Render(pad(s[0], 14)) + stSubtle.Render(s[1]) + "\n")
	}

	b.WriteString("\n" + stSubtle.Render("  A 403 counts as live: the credentials worked, RBAC declined the verb.") + "\n")
	b.WriteString(stSubtle.Render("  Deleting a context also removes its cluster and user entries when no") + "\n")
	b.WriteString(stSubtle.Render("  surviving context references them. Every write is preceded by a backup") + "\n")
	b.WriteString(stSubtle.Render("  of the whole kubeconfig chain under "+shortPath(m.store.BackupDir)+".") + "\n")
	b.WriteString("\n  " + stSubtle.Render("press any key to go back"))

	return stBox.Width(m.width - 4).Render(b.String())
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
