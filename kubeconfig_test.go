package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
)

const singleFile = `apiVersion: v1
kind: Config
current-context: prod
clusters:
- name: shared-cluster
  cluster: {server: https://shared.example.com}
- name: lonely-cluster
  cluster: {server: https://lonely.example.com}
contexts:
- name: prod
  context: {cluster: shared-cluster, user: shared-user, namespace: payments}
- name: prod-admin
  context: {cluster: shared-cluster, user: admin-user}
- name: scratch
  context: {cluster: lonely-cluster, user: lonely-user}
users:
- name: shared-user
  user: {token: aaa}
- name: admin-user
  user: {token: bbb}
- name: lonely-user
  user: {token: ccc}
`

func writeCfg(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func newTestStore(t *testing.T, files ...string) *Store {
	t.Helper()
	t.Setenv("KUBECONFIG", strings.Join(files, string(os.PathListSeparator)))
	s, err := NewStore(filepath.Join(t.TempDir(), "backups"), 10, true)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPlanDeleteKeepsSharedClusterAndUser(t *testing.T) {
	dir := t.TempDir()
	s := newTestStore(t, writeCfg(t, dir, "config", singleFile))

	plan := s.PlanDelete([]string{"prod"})

	if got := plan.OrphanClusters; len(got) != 0 {
		t.Errorf("shared-cluster is still used by prod-admin, should not be an orphan; got %v", got)
	}
	if len(plan.KeptClusters) != 1 || plan.KeptClusters[0] != "shared-cluster" {
		t.Errorf("expected shared-cluster reported as kept, got %v", plan.KeptClusters)
	}
	// shared-user is referenced only by prod, so it does become an orphan.
	if len(plan.OrphanUsers) != 1 || plan.OrphanUsers[0] != "shared-user" {
		t.Errorf("expected shared-user orphaned, got %v", plan.OrphanUsers)
	}
	if !plan.ClearsCurrent {
		t.Error("deleting the current context should be flagged")
	}
}

func TestPlanDeleteOrphansUnsharedEntries(t *testing.T) {
	dir := t.TempDir()
	s := newTestStore(t, writeCfg(t, dir, "config", singleFile))

	plan := s.PlanDelete([]string{"scratch"})
	if len(plan.OrphanClusters) != 1 || plan.OrphanClusters[0] != "lonely-cluster" {
		t.Errorf("expected lonely-cluster orphaned, got %v", plan.OrphanClusters)
	}
	if len(plan.OrphanUsers) != 1 || plan.OrphanUsers[0] != "lonely-user" {
		t.Errorf("expected lonely-user orphaned, got %v", plan.OrphanUsers)
	}
	if plan.ClearsCurrent {
		t.Error("scratch is not the current context")
	}
}

func TestApplyRemovesOrphansFromDisk(t *testing.T) {
	dir := t.TempDir()
	path := writeCfg(t, dir, "config", singleFile)
	s := newTestStore(t, path)

	if err := s.Apply(s.PlanDelete([]string{"scratch"})); err != nil {
		t.Fatal(err)
	}

	on, err := clientcmd.LoadFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := on.Contexts["scratch"]; ok {
		t.Error("context scratch still present on disk")
	}
	if _, ok := on.Clusters["lonely-cluster"]; ok {
		t.Error("orphaned cluster still present on disk")
	}
	if _, ok := on.AuthInfos["lonely-user"]; ok {
		t.Error("orphaned user still present on disk")
	}
	if _, ok := on.Clusters["shared-cluster"]; !ok {
		t.Error("shared cluster was removed but is still referenced")
	}
	if len(on.Contexts) != 2 {
		t.Errorf("expected 2 surviving contexts, got %d", len(on.Contexts))
	}
}

// The multi-file case is the one that bites people: a context defined in the
// second KUBECONFIG file must be deleted from that file, not from the first.
func TestDeleteWritesToTheDefiningFile(t *testing.T) {
	dir := t.TempDir()
	first := writeCfg(t, dir, "primary", `apiVersion: v1
kind: Config
current-context: alpha
clusters:
- name: c-alpha
  cluster: {server: https://alpha.example.com}
contexts:
- name: alpha
  context: {cluster: c-alpha, user: u-alpha}
users:
- name: u-alpha
  user: {token: a}
`)
	second := writeCfg(t, dir, "secondary", `apiVersion: v1
kind: Config
clusters:
- name: c-beta
  cluster: {server: https://beta.example.com}
contexts:
- name: beta
  context: {cluster: c-beta, user: u-beta}
users:
- name: u-beta
  user: {token: b}
`)

	s := newTestStore(t, first, second)

	if got := len(s.Contexts()); got != 2 {
		t.Fatalf("expected merged view of 2 contexts, got %d", got)
	}
	for _, c := range s.Contexts() {
		if c.Name == "beta" && c.SourceFile != second {
			t.Errorf("beta should be attributed to %s, got %s", second, c.SourceFile)
		}
	}

	if err := s.Apply(s.PlanDelete([]string{"beta"})); err != nil {
		t.Fatal(err)
	}

	onSecond, err := clientcmd.LoadFromFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := onSecond.Contexts["beta"]; ok {
		t.Error("beta was not removed from the file that defined it")
	}

	onFirst, err := clientcmd.LoadFromFile(first)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := onFirst.Contexts["alpha"]; !ok {
		t.Error("alpha was collateral damage in the primary file")
	}
	if onFirst.CurrentContext != "alpha" {
		t.Errorf("primary current-context changed to %q", onFirst.CurrentContext)
	}
}

func TestRenameCarriesCurrentContext(t *testing.T) {
	dir := t.TempDir()
	path := writeCfg(t, dir, "config", singleFile)
	s := newTestStore(t, path)

	if err := s.Rename("prod", "production-eu"); err != nil {
		t.Fatal(err)
	}
	if s.CurrentContext() != "production-eu" {
		t.Errorf("current-context should follow the rename, got %q", s.CurrentContext())
	}

	on, _ := clientcmd.LoadFromFile(path)
	if _, ok := on.Contexts["prod"]; ok {
		t.Error("old name still on disk")
	}
	c, ok := on.Contexts["production-eu"]
	if !ok {
		t.Fatal("new name missing on disk")
	}
	if c.Namespace != "payments" {
		t.Errorf("namespace lost in rename, got %q", c.Namespace)
	}
}

func TestRenameRejectsCollision(t *testing.T) {
	dir := t.TempDir()
	s := newTestStore(t, writeCfg(t, dir, "config", singleFile))
	if err := s.Rename("prod", "scratch"); err == nil {
		t.Error("renaming onto an existing context should fail")
	}
}

func TestSetNamespacePersists(t *testing.T) {
	dir := t.TempDir()
	path := writeCfg(t, dir, "config", singleFile)
	s := newTestStore(t, path)

	if err := s.SetNamespace("scratch", "kube-system"); err != nil {
		t.Fatal(err)
	}
	on, _ := clientcmd.LoadFromFile(path)
	if got := on.Contexts["scratch"].Namespace; got != "kube-system" {
		t.Errorf("namespace = %q, want kube-system", got)
	}
}

func TestBackupSnapshotsEveryFileBeforeWrite(t *testing.T) {
	dir := t.TempDir()
	first := writeCfg(t, dir, "primary", singleFile)
	second := writeCfg(t, dir, "secondary", `apiVersion: v1
kind: Config
contexts:
- name: beta
  context: {cluster: c-beta, user: u-beta}
clusters:
- name: c-beta
  cluster: {server: https://beta.example.com}
users:
- name: u-beta
  user: {token: b}
`)
	t.Setenv("KUBECONFIG", first+string(os.PathListSeparator)+second)
	backupRoot := filepath.Join(t.TempDir(), "backups")
	s, err := NewStore(backupRoot, 10, true)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Apply(s.PlanDelete([]string{"scratch"})); err != nil {
		t.Fatal(err)
	}

	snap := s.LastBackupDir()
	if snap == "" {
		t.Fatal("no backup directory created")
	}
	entries, err := os.ReadDir(snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected both kubeconfig files snapshotted, got %d", len(entries))
	}
	// The snapshot must hold the pre-delete content.
	body, err := os.ReadFile(filepath.Join(snap, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "scratch") {
		t.Error("backup does not contain the pre-delete state")
	}
}

func TestBackupPruneKeepsNewest(t *testing.T) {
	dir := t.TempDir()
	backupRoot := filepath.Join(t.TempDir(), "backups")
	if err := os.MkdirAll(backupRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"20200101-000000", "20210101-000000", "20220101-000000"} {
		if err := os.MkdirAll(filepath.Join(backupRoot, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("KUBECONFIG", writeCfg(t, dir, "config", singleFile))
	s, err := NewStore(backupRoot, 2, true)
	if err != nil {
		t.Fatal(err)
	}
	s.pruneBackups()

	entries, _ := os.ReadDir(backupRoot)
	if len(entries) != 2 {
		t.Fatalf("expected 2 snapshots retained, got %d", len(entries))
	}
	if entries[0].Name() != "20210101-000000" {
		t.Errorf("pruned the wrong end: %v", entries[0].Name())
	}
}

func TestBackupsDisabled(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KUBECONFIG", writeCfg(t, dir, "config", singleFile))
	backupRoot := filepath.Join(t.TempDir(), "backups")
	s, err := NewStore(backupRoot, 10, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(s.PlanDelete([]string{"scratch"})); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(backupRoot); !os.IsNotExist(err) {
		t.Error("--no-backup should not create a backup directory")
	}
}

func TestContextsReportsDefaultNamespaceAndAuthKind(t *testing.T) {
	dir := t.TempDir()
	s := newTestStore(t, writeCfg(t, dir, "config", singleFile))
	byName := map[string]ContextInfo{}
	for _, c := range s.Contexts() {
		byName[c.Name] = c
	}
	if byName["prod-admin"].Namespace != "default" {
		t.Errorf("unset namespace should render as default, got %q", byName["prod-admin"].Namespace)
	}
	if byName["prod"].AuthKind != "token" {
		t.Errorf("AuthKind = %q, want token", byName["prod"].AuthKind)
	}
	if byName["prod"].Server != "https://shared.example.com" {
		t.Errorf("Server = %q", byName["prod"].Server)
	}
}
