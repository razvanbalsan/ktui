package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"k8s.io/client-go/tools/clientcmd"
)

// The TUI is driven directly through Update/View, so these tests need no
// terminal and never touch the network (probing is off).

func runes(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func special(t tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: t} }

func send(t *testing.T, m model, msgs ...tea.Msg) model {
	t.Helper()
	for _, msg := range msgs {
		next, _ := m.Update(msg)
		m = next.(model)
	}
	return m
}

func testModel(t *testing.T) (model, string) {
	t.Helper()
	dir := t.TempDir()
	path := writeCfg(t, dir, "config", singleFile)
	t.Setenv("KUBECONFIG", path)
	s, err := NewStore(filepath.Join(t.TempDir(), "backups"), 10, true)
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(s, Options{Probe: false, ProbeTimeout: 0})
	m = send(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	return m, path
}

func TestSpaceKeyIsRecognised(t *testing.T) {
	// Guards the assumption the list handler makes about how space arrives.
	if got := special(tea.KeySpace).String(); got != " " {
		t.Fatalf("KeySpace.String() = %q, want %q", got, " ")
	}
}

func TestListRendersAllContextsAndMarksCurrent(t *testing.T) {
	m, _ := testModel(t)
	out := m.View()
	for _, want := range []string{"prod", "prod-admin", "scratch", "payments"} {
		if !strings.Contains(out, want) {
			t.Errorf("view missing %q", want)
		}
	}
	if !strings.Contains(out, "current: ") {
		t.Error("header should name the current context")
	}
}

func TestCursorMovementStaysInBounds(t *testing.T) {
	m, _ := testModel(t)
	m = send(t, m, runes("k"), runes("k")) // up past the top
	if m.cursor != 0 {
		t.Errorf("cursor went above the first row: %d", m.cursor)
	}
	m = send(t, m, runes("G"), runes("j"), runes("j")) // down past the bottom
	if m.cursor != len(m.view)-1 {
		t.Errorf("cursor = %d, want %d", m.cursor, len(m.view)-1)
	}
}

func TestFilterNarrowsAndClears(t *testing.T) {
	m, _ := testModel(t)
	m = send(t, m, runes("/"), runes("s"), runes("c"))
	if len(m.view) != 1 || m.contexts[m.view[0]].Name != "scratch" {
		t.Fatalf("filter 'sc' should leave only scratch, got %d rows", len(m.view))
	}
	m = send(t, m, special(tea.KeyEsc))
	if len(m.view) != 3 {
		t.Errorf("esc should restore all rows, got %d", len(m.view))
	}
}

func TestFilterMatchesServerNotJustName(t *testing.T) {
	m, _ := testModel(t)
	m = send(t, m, runes("/"), runes("l"), runes("o"), runes("n"))
	if len(m.view) != 1 || m.contexts[m.view[0]].Name != "scratch" {
		t.Errorf("filtering on the server host should find scratch, got %d rows", len(m.view))
	}
}

func TestSwitchContextPersists(t *testing.T) {
	m, path := testModel(t)
	// rows are sorted: prod, prod-admin, scratch
	m = send(t, m, runes("G"), special(tea.KeyEnter))

	on, _ := clientcmd.LoadFromFile(path)
	if on.CurrentContext != "scratch" {
		t.Errorf("current-context on disk = %q, want scratch", on.CurrentContext)
	}
	if !strings.Contains(m.status, "switched to scratch") {
		t.Errorf("status = %q", m.status)
	}
}

func TestSelectionThenDeleteAppliesToSelection(t *testing.T) {
	m, path := testModel(t)
	// select prod-admin and scratch, leave the cursor elsewhere
	m = send(t, m, runes("j"), special(tea.KeySpace), special(tea.KeySpace))
	if len(m.selected) != 2 {
		t.Fatalf("expected 2 selected, got %d", len(m.selected))
	}
	m = send(t, m, runes("g")) // cursor back to prod, which is NOT selected
	m = send(t, m, runes("d"))
	if m.mode != viewConfirmDelete {
		t.Fatal("d should open the confirmation")
	}
	if len(m.plan.Contexts) != 2 {
		t.Errorf("plan should cover the selection, not the cursor row: %v", m.plan.Contexts)
	}

	confirm := m.View()
	if !strings.Contains(confirm, "lonely-cluster") {
		t.Error("confirmation should name the orphaned cluster")
	}
	if !strings.Contains(confirm, "shared-cluster") {
		t.Error("confirmation should name the cluster being kept")
	}

	m = send(t, m, runes("y"))
	on, _ := clientcmd.LoadFromFile(path)
	if len(on.Contexts) != 1 {
		t.Errorf("expected 1 surviving context, got %d", len(on.Contexts))
	}
	if _, ok := on.Contexts["prod"]; !ok {
		t.Error("prod should have survived")
	}
	if len(m.selected) != 0 {
		t.Error("selection should be cleared after delete")
	}
}

func TestDeleteCancelChangesNothing(t *testing.T) {
	m, path := testModel(t)
	m = send(t, m, runes("d"), runes("n"))
	if m.mode != viewList {
		t.Error("n should return to the list")
	}
	on, _ := clientcmd.LoadFromFile(path)
	if len(on.Contexts) != 3 {
		t.Errorf("cancel must not write; got %d contexts", len(on.Contexts))
	}
}

func TestDeleteConfirmWarnsAboutCurrentContext(t *testing.T) {
	m, _ := testModel(t)
	m = send(t, m, runes("g"), runes("d")) // prod is current
	if !m.plan.ClearsCurrent {
		t.Fatal("plan should flag that the current context is going away")
	}
	if !strings.Contains(m.View(), "current context") {
		t.Error("confirmation should warn about losing the current context")
	}
}

func TestRenameFlow(t *testing.T) {
	m, path := testModel(t)
	m = send(t, m, runes("G"), runes("r"))
	if m.mode != viewRename || m.renameFrom != "scratch" {
		t.Fatalf("rename should target scratch, got %q", m.renameFrom)
	}
	// clear the prefilled value and type a new name
	for i := 0; i < len("scratch"); i++ {
		m = send(t, m, special(tea.KeyBackspace))
	}
	m = send(t, m, runes("dev"), special(tea.KeyEnter))

	on, _ := clientcmd.LoadFromFile(path)
	if _, ok := on.Contexts["dev"]; !ok {
		t.Fatalf("rename did not persist; contexts: %v", keysOf(on.Contexts))
	}
	if c, _ := m.current(); c.Name != "dev" {
		t.Errorf("cursor should follow the renamed context, got %q", c.Name)
	}
}

func TestRenameEscapeDoesNotWrite(t *testing.T) {
	m, path := testModel(t)
	m = send(t, m, runes("r"), runes("x"), special(tea.KeyEsc))
	on, _ := clientcmd.LoadFromFile(path)
	if _, ok := on.Contexts["prod"]; !ok {
		t.Error("esc during rename should leave the config untouched")
	}
	if m.mode != viewList {
		t.Error("esc should return to the list")
	}
}

func TestRenameCollisionSurfacesError(t *testing.T) {
	m, _ := testModel(t)
	m = send(t, m, runes("g"), runes("r"))
	for i := 0; i < len("prod"); i++ {
		m = send(t, m, special(tea.KeyBackspace))
	}
	m = send(t, m, runes("scratch"), special(tea.KeyEnter))
	if !m.statusErr || !strings.Contains(m.status, "already exists") {
		t.Errorf("expected a collision error in the status bar, got %q", m.status)
	}
}

func TestSelectAllToggles(t *testing.T) {
	m, _ := testModel(t)
	m = send(t, m, runes("a"))
	if len(m.selected) != 3 {
		t.Fatalf("a should select all 3, got %d", len(m.selected))
	}
	m = send(t, m, runes("a"))
	if len(m.selected) != 0 {
		t.Errorf("a again should clear the selection, got %d", len(m.selected))
	}
}

func TestSelectAllRespectsFilter(t *testing.T) {
	m, _ := testModel(t)
	m = send(t, m, runes("/"), runes("sc"), special(tea.KeyEnter), runes("a"))
	if len(m.selected) != 1 {
		t.Errorf("a should only select filtered rows, got %d", len(m.selected))
	}
}

func TestHelpOpensAndCloses(t *testing.T) {
	m, _ := testModel(t)
	m = send(t, m, runes("?"))
	if m.mode != viewHelp {
		t.Fatal("? should open help")
	}
	if !strings.Contains(m.View(), "auth expired") {
		t.Error("help should document the status vocabulary")
	}
	m = send(t, m, runes("x"))
	if m.mode != viewList {
		t.Error("any key should dismiss help")
	}
}

func TestNamespaceViewHandlesProbeFailureGracefully(t *testing.T) {
	m, _ := testModel(t)
	m.probes["prod"] = ProbeResult{Context: "prod", State: ProbeUnreachable}
	m = send(t, m, runes("g"), runes("n"))
	if m.mode == viewNamespaces {
		t.Error("should refuse to open the namespace picker for an unreachable context")
	}
	if !m.statusErr {
		t.Error("expected an explanatory error")
	}
}

func TestNamespaceSelectionWritesConfig(t *testing.T) {
	m, path := testModel(t)
	m = send(t, m, runes("G"), runes("n")) // scratch
	if m.mode != viewNamespaces {
		t.Fatal("n should open the namespace picker")
	}
	// Feed the result the network call would have produced.
	m = send(t, m, nsMsg{ctxName: "scratch", items: []string{"default", "kube-system", "team-a"}})
	m = send(t, m, runes("j"), special(tea.KeyEnter))

	on, _ := clientcmd.LoadFromFile(path)
	if got := on.Contexts["scratch"].Namespace; got != "kube-system" {
		t.Errorf("namespace = %q, want kube-system", got)
	}
	if m.mode != viewList {
		t.Error("should return to the list after setting a namespace")
	}
}

func TestNamespaceCursorStartsOnConfiguredNamespace(t *testing.T) {
	m, _ := testModel(t)
	m = send(t, m, runes("g"), runes("n")) // prod, namespace payments
	m = send(t, m, nsMsg{ctxName: "prod", items: []string{"default", "payments", "kube-system"}})
	if got := m.nsItems[m.nsView[m.nsCursor]]; got != "payments" {
		t.Errorf("cursor started on %q, want payments", got)
	}
}

func TestNamespaceErrorReturnsToList(t *testing.T) {
	m, _ := testModel(t)
	m = send(t, m, runes("n"))
	m = send(t, m, nsMsg{ctxName: "prod", err: os.ErrDeadlineExceeded})
	if m.mode != viewList || !m.statusErr {
		t.Error("a namespace fetch error should drop back to the list with an error")
	}
}

func TestProbeResultRendersInList(t *testing.T) {
	m, _ := testModel(t)
	m.opts.Probe = true
	m = send(t, m, probeMsg{Context: "prod", State: ProbeAuthExpired, Detail: "Unauthorized"})
	if !strings.Contains(m.View(), "auth expired") {
		t.Error("probe state should appear in the list")
	}
	if !strings.Contains(m.View(), "Unauthorized") {
		t.Error("probe detail should appear in the detail pane for the highlighted row")
	}
}

func TestViewSurvivesNarrowTerminals(t *testing.T) {
	m, _ := testModel(t)
	for _, w := range []int{20, 40, 60, 80, 200} {
		for _, h := range []int{8, 12, 24, 60} {
			mm := send(t, m, tea.WindowSizeMsg{Width: w, Height: h})
			for _, mode := range []viewMode{viewList, viewHelp, viewConfirmDelete, viewNamespaces} {
				mm.mode = mode
				mm.plan = mm.store.PlanDelete([]string{"scratch"})
				if out := mm.View(); out == "" && mode != viewList {
					t.Errorf("empty render at %dx%d in mode %v", w, h, mode)
				}
			}
		}
	}
}

func TestQuitSetsFlag(t *testing.T) {
	m, _ := testModel(t)
	m = send(t, m, runes("q"))
	if !m.quitting {
		t.Error("q should quit")
	}
	if m.View() != "" {
		t.Error("view should be empty once quitting so the alt screen restores cleanly")
	}
}

func keysOf[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
