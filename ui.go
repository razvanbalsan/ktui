package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

type viewMode int

const (
	viewList viewMode = iota
	viewNamespaces
	viewConfirmDelete
	viewConfirmQuit
	viewRename
	viewHelp
)

// probeSem bounds how many API servers we talk to at once, so opening the TUI
// against a fifty-context kubeconfig does not fan out fifty TLS handshakes.
var probeSem = make(chan struct{}, 8)

type (
	probeMsg       ProbeResult
	nsMsg          struct {
		ctxName string
		items   []string
		err     error
	}
	statusMsg struct {
		text  string
		isErr bool
		seq   int
	}
	clearStatusMsg struct{ seq int }
)

type model struct {
	store *Store
	opts  Options

	contexts []ContextInfo
	view     []int // indices into contexts, after filtering
	cursor   int
	offset   int
	selected map[string]bool
	probes   map[string]ProbeResult

	mode        viewMode
	prevMode    viewMode
	filter      textinput.Model
	filtering   bool
	rename      textinput.Model
	renameFrom  string
	nsFilter    textinput.Model
	nsFiltering bool
	nsItems     []string
	nsView      []int
	nsCursor    int
	nsOffset    int
	nsContext   string
	nsLoading   bool

	plan DeletePlan

	status    string
	statusErr bool
	statusSeq int

	width, height int
	quitting      bool
}

func newModel(s *Store, opts Options) model {
	f := textinput.New()
	f.Prompt = "/"
	f.Placeholder = "filter"
	f.CharLimit = 120

	r := textinput.New()
	r.Prompt = "› "
	r.CharLimit = 253

	nf := textinput.New()
	nf.Prompt = "/"
	nf.Placeholder = "filter namespaces"
	nf.CharLimit = 120

	m := model{
		store:    s,
		opts:     opts,
		selected: map[string]bool{},
		probes:   map[string]ProbeResult{},
		filter:   f,
		rename:   r,
		nsFilter: nf,
		width:    80,
		height:   24,
	}
	m.reload()
	return m
}

func (m *model) reload() {
	m.contexts = m.store.Contexts()
	m.applyFilter()
}

func (m *model) applyFilter() {
	q := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	m.view = m.view[:0]
	for i, c := range m.contexts {
		if q == "" || strings.Contains(strings.ToLower(c.Name), q) ||
			strings.Contains(strings.ToLower(c.Cluster), q) ||
			strings.Contains(strings.ToLower(c.Server), q) {
			m.view = append(m.view, i)
		}
	}
	m.clampCursor()
}

func (m *model) clampCursor() {
	if m.cursor >= len(m.view) {
		m.cursor = len(m.view) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

func (m *model) current() (ContextInfo, bool) {
	if m.cursor < 0 || m.cursor >= len(m.view) {
		return ContextInfo{}, false
	}
	return m.contexts[m.view[m.cursor]], true
}

// targets returns the contexts an action applies to: everything explicitly
// selected, or the row under the cursor when nothing is selected.
func (m *model) targets() []string {
	if len(m.selected) > 0 {
		out := make([]string, 0, len(m.selected))
		for n := range m.selected {
			out = append(out, n)
		}
		sort.Strings(out)
		return out
	}
	if c, ok := m.current(); ok {
		return []string{c.Name}
	}
	return nil
}

func (m *model) setStatus(text string, isErr bool) tea.Cmd {
	m.statusSeq++
	m.status = text
	m.statusErr = isErr
	seq := m.statusSeq
	return tea.Tick(6*time.Second, func(time.Time) tea.Msg {
		return clearStatusMsg{seq: seq}
	})
}

func probeCmd(name string, timeout time.Duration) tea.Cmd {
	return func() tea.Msg {
		probeSem <- struct{}{}
		defer func() { <-probeSem }()
		return probeMsg(Probe(context.Background(), name, timeout))
	}
}

func (m *model) probeAll() tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(m.contexts))
	for _, c := range m.contexts {
		m.probes[c.Name] = ProbeResult{Context: c.Name, State: ProbePending}
		cmds = append(cmds, probeCmd(c.Name, m.opts.ProbeTimeout))
	}
	return tea.Batch(cmds...)
}

func nsCmd(ctxName string, timeout time.Duration) tea.Cmd {
	return func() tea.Msg {
		items, err := ListNamespaces(context.Background(), ctxName, timeout)
		return nsMsg{ctxName: ctxName, items: items, err: err}
	}
}

func (m model) Init() tea.Cmd {
	if m.opts.Probe {
		return m.probeAll()
	}
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case probeMsg:
		m.probes[msg.Context] = ProbeResult(msg)
		return m, nil

	case nsMsg:
		m.nsLoading = false
		if msg.err != nil {
			m.mode = viewList
			return m, m.setStatus("namespaces: "+firstLine(msg.err.Error()), true)
		}
		m.nsItems = msg.items
		m.applyNsFilter()
		m.nsCursor, m.nsOffset = 0, 0
		// Park the cursor on the namespace already configured for this context.
		for i, idx := range m.nsView {
			if c, ok := m.contextByName(msg.ctxName); ok && m.nsItems[idx] == c.Namespace {
				m.nsCursor = i
				break
			}
		}
		return m, nil

	case clearStatusMsg:
		if msg.seq == m.statusSeq {
			m.status = ""
			m.statusErr = false
		}
		return m, nil

	case statusMsg:
		return m, m.setStatus(msg.text, msg.isErr)

	case tea.KeyMsg:
		switch m.mode {
		case viewList:
			return m.updateList(msg)
		case viewNamespaces:
			return m.updateNamespaces(msg)
		case viewConfirmDelete:
			return m.updateConfirm(msg)
		case viewConfirmQuit:
			return m.updateConfirmQuit(msg)
		case viewRename:
			return m.updateRename(msg)
		case viewHelp:
			m.mode = m.prevMode
			return m, nil
		}
	}
	return m, nil
}

func (m *model) contextByName(name string) (ContextInfo, bool) {
	for _, c := range m.contexts {
		if c.Name == name {
			return c, true
		}
	}
	return ContextInfo{}, false
}

func (m model) updateList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.filtering {
		switch msg.String() {
		case "esc":
			m.filtering = false
			m.filter.SetValue("")
			m.filter.Blur()
			m.applyFilter()
			return m, nil
		case "enter":
			m.filtering = false
			m.filter.Blur()
			return m, nil
		}
		var cmd tea.Cmd
		m.filter, cmd = m.filter.Update(msg)
		m.applyFilter()
		return m, cmd
	}

	switch msg.String() {
	case "q":
		m.prevMode = m.mode
		m.mode = viewConfirmQuit
		return m, nil

	case "ctrl+c":
		// The universal interrupt stays immediate: anyone reaching for it
		// wants out now, not another prompt.
		m.quitting = true
		return m, tea.Quit

	case "j", "down":
		if m.cursor < len(m.view)-1 {
			m.cursor++
		}
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "g", "home":
		m.cursor = 0
	case "G", "end":
		m.cursor = len(m.view) - 1
		m.clampCursor()
	case "ctrl+d", "pgdown":
		m.cursor += m.listHeight() / 2
		m.clampCursor()
	case "ctrl+u", "pgup":
		m.cursor -= m.listHeight() / 2
		m.clampCursor()

	case "/":
		m.filtering = true
		m.filter.Focus()
		return m, textinput.Blink

	case " ":
		if c, ok := m.current(); ok {
			if m.selected[c.Name] {
				delete(m.selected, c.Name)
			} else {
				m.selected[c.Name] = true
			}
			if m.cursor < len(m.view)-1 {
				m.cursor++
			}
		}

	case "a":
		// Select or clear everything currently visible.
		allSelected := len(m.view) > 0
		for _, i := range m.view {
			if !m.selected[m.contexts[i].Name] {
				allSelected = false
				break
			}
		}
		for _, i := range m.view {
			if allSelected {
				delete(m.selected, m.contexts[i].Name)
			} else {
				m.selected[m.contexts[i].Name] = true
			}
		}

	case "esc":
		if len(m.selected) > 0 {
			m.selected = map[string]bool{}
		} else if m.filter.Value() != "" {
			m.filter.SetValue("")
			m.applyFilter()
		}

	case "enter":
		c, ok := m.current()
		if !ok {
			return m, nil
		}
		if err := m.store.SetCurrent(c.Name); err != nil {
			return m, m.setStatus(err.Error(), true)
		}
		m.reload()
		if m.opts.ExitOnSwitch {
			m.quitting = true
			fmt.Println(c.Name)
			return m, tea.Quit
		}
		return m, m.setStatus("switched to "+c.Name, false)

	case "n":
		c, ok := m.current()
		if !ok {
			return m, nil
		}
		if st, seen := m.probes[c.Name]; seen && (st.State == ProbeUnreachable || st.State == ProbeCredError) {
			return m, m.setStatus("cannot list namespaces: context is "+st.State.Label(), true)
		}
		m.mode = viewNamespaces
		m.nsContext = c.Name
		m.nsItems = nil
		m.nsView = nil
		m.nsLoading = true
		m.nsFilter.SetValue("")
		m.nsFiltering = false
		return m, nsCmd(c.Name, m.opts.ProbeTimeout)

	case "r":
		c, ok := m.current()
		if !ok {
			return m, nil
		}
		m.mode = viewRename
		m.renameFrom = c.Name
		m.rename.SetValue(c.Name)
		m.rename.CursorEnd()
		m.rename.Focus()
		return m, textinput.Blink

	case "d", "delete":
		names := m.targets()
		if len(names) == 0 {
			return m, nil
		}
		m.plan = m.store.PlanDelete(names)
		m.mode = viewConfirmDelete

	case "p":
		return m, m.probeAll()

	case "?":
		m.prevMode = m.mode
		m.mode = viewHelp
	}
	return m, nil
}

func (m model) updateConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		n := len(m.plan.Contexts)
		if err := m.store.Apply(m.plan); err != nil {
			m.mode = viewList
			return m, m.setStatus(err.Error(), true)
		}
		for _, name := range m.plan.Contexts {
			delete(m.selected, name)
			delete(m.probes, name)
		}
		m.reload()
		m.mode = viewList
		msgTxt := fmt.Sprintf("deleted %d context(s), %d cluster(s), %d user(s)",
			n, len(m.plan.OrphanClusters), len(m.plan.OrphanUsers))
		if b := m.store.LastBackupDir(); b != "" {
			msgTxt += " · backup " + shortPath(b)
		}
		return m, m.setStatus(msgTxt, false)
	case "n", "N", "esc", "q":
		m.mode = viewList
	}
	return m, nil
}

// updateConfirmQuit answers the quit prompt. A second q confirms, so leaving is
// still two keystrokes for anyone who knows where they are going, while a single
// stray q no longer drops the session.
func (m model) updateConfirmQuit(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y", "q", "enter", "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "n", "N", "esc":
		m.mode = m.prevMode
	}
	return m, nil
}

func (m model) updateRename(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = viewList
		m.rename.Blur()
		return m, nil
	case "enter":
		newName := strings.TrimSpace(m.rename.Value())
		m.mode = viewList
		m.rename.Blur()
		if newName == "" || newName == m.renameFrom {
			return m, nil
		}
		if err := m.store.Rename(m.renameFrom, newName); err != nil {
			return m, m.setStatus(err.Error(), true)
		}
		if m.selected[m.renameFrom] {
			delete(m.selected, m.renameFrom)
			m.selected[newName] = true
		}
		if pr, ok := m.probes[m.renameFrom]; ok {
			pr.Context = newName
			m.probes[newName] = pr
			delete(m.probes, m.renameFrom)
		}
		m.reload()
		for i, idx := range m.view {
			if m.contexts[idx].Name == newName {
				m.cursor = i
				break
			}
		}
		return m, m.setStatus("renamed to "+newName, false)
	}
	var cmd tea.Cmd
	m.rename, cmd = m.rename.Update(msg)
	return m, cmd
}

func (m *model) applyNsFilter() {
	q := strings.ToLower(strings.TrimSpace(m.nsFilter.Value()))
	m.nsView = m.nsView[:0]
	for i, n := range m.nsItems {
		if q == "" || strings.Contains(strings.ToLower(n), q) {
			m.nsView = append(m.nsView, i)
		}
	}
	if m.nsCursor >= len(m.nsView) {
		m.nsCursor = len(m.nsView) - 1
	}
	if m.nsCursor < 0 {
		m.nsCursor = 0
	}
}

func (m model) updateNamespaces(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.nsFiltering {
		switch msg.String() {
		case "esc":
			m.nsFiltering = false
			m.nsFilter.SetValue("")
			m.nsFilter.Blur()
			m.applyNsFilter()
			return m, nil
		case "enter":
			m.nsFiltering = false
			m.nsFilter.Blur()
			return m, nil
		}
		var cmd tea.Cmd
		m.nsFilter, cmd = m.nsFilter.Update(msg)
		m.applyNsFilter()
		return m, cmd
	}

	switch msg.String() {
	case "esc", "q":
		m.mode = viewList
	case "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "j", "down":
		if m.nsCursor < len(m.nsView)-1 {
			m.nsCursor++
		}
	case "k", "up":
		if m.nsCursor > 0 {
			m.nsCursor--
		}
	case "g":
		m.nsCursor = 0
	case "G":
		m.nsCursor = len(m.nsView) - 1
	case "/":
		m.nsFiltering = true
		m.nsFilter.Focus()
		return m, textinput.Blink
	case "enter":
		if m.nsCursor < 0 || m.nsCursor >= len(m.nsView) {
			return m, nil
		}
		ns := m.nsItems[m.nsView[m.nsCursor]]
		if err := m.store.SetNamespace(m.nsContext, ns); err != nil {
			return m, m.setStatus(err.Error(), true)
		}
		m.reload()
		m.mode = viewList
		return m, m.setStatus(fmt.Sprintf("%s → namespace %s", m.nsContext, ns), false)
	case "?":
		m.prevMode = m.mode
		m.mode = viewHelp
	}
	return m, nil
}
