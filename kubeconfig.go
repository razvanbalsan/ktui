package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// ContextInfo is the flattened view of one kubeconfig context that the UI renders.
type ContextInfo struct {
	Name       string
	Cluster    string
	AuthInfo   string
	Namespace  string
	Server     string
	AuthKind   string // "exec:aws", "token", "client-cert", ...
	IsCurrent  bool
	SourceFile string
}

// Store owns the merged kubeconfig and every mutation applied to it.
//
// All writes go through clientcmd.ModifyConfig, which distributes each changed
// stanza back to the file it was loaded from rather than collapsing a merged
// KUBECONFIG chain into its first file.
type Store struct {
	pathOptions *clientcmd.PathOptions
	cfg         *clientcmdapi.Config
	BackupDir   string
	KeepBackups int
	Backups     bool
}

func NewStore(backupDir string, keep int, backups bool) (*Store, error) {
	po := clientcmd.NewDefaultPathOptions()
	s := &Store{
		pathOptions: po,
		BackupDir:   backupDir,
		KeepBackups: keep,
		Backups:     backups,
	}
	return s, s.Reload()
}

func (s *Store) Reload() error {
	cfg, err := s.pathOptions.GetStartingConfig()
	if err != nil {
		return fmt.Errorf("reading kubeconfig: %w", err)
	}
	s.cfg = cfg
	return nil
}

// Files lists the kubeconfig files in KUBECONFIG precedence order that exist on disk.
func (s *Store) Files() []string {
	var out []string
	for _, f := range s.pathOptions.GetLoadingPrecedence() {
		if _, err := os.Stat(f); err == nil {
			out = append(out, f)
		}
	}
	return out
}

// sourceOf maps each context name to the first file in precedence order that defines it.
func (s *Store) sourceOf() map[string]string {
	src := map[string]string{}
	for _, f := range s.Files() {
		c, err := clientcmd.LoadFromFile(f)
		if err != nil {
			continue
		}
		for name := range c.Contexts {
			if _, seen := src[name]; !seen {
				src[name] = f
			}
		}
	}
	return src
}

func authKind(a *clientcmdapi.AuthInfo) string {
	switch {
	case a == nil:
		return "none"
	case a.Exec != nil:
		return "exec:" + filepath.Base(a.Exec.Command)
	case a.AuthProvider != nil:
		return "authprovider:" + a.AuthProvider.Name
	case a.ClientCertificate != "" || len(a.ClientCertificateData) > 0:
		return "client-cert"
	case a.Token != "" || a.TokenFile != "":
		return "token"
	case a.Username != "":
		return "basic"
	}
	return "none"
}

func (s *Store) Contexts() []ContextInfo {
	src := s.sourceOf()
	out := make([]ContextInfo, 0, len(s.cfg.Contexts))
	for name, c := range s.cfg.Contexts {
		ci := ContextInfo{
			Name:       name,
			Cluster:    c.Cluster,
			AuthInfo:   c.AuthInfo,
			Namespace:  c.Namespace,
			IsCurrent:  name == s.cfg.CurrentContext,
			SourceFile: src[name],
		}
		if ci.Namespace == "" {
			ci.Namespace = "default"
		}
		if cl, ok := s.cfg.Clusters[c.Cluster]; ok && cl != nil {
			ci.Server = cl.Server
		}
		ci.AuthKind = authKind(s.cfg.AuthInfos[c.AuthInfo])
		out = append(out, ci)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *Store) CurrentContext() string { return s.cfg.CurrentContext }

// DeletePlan is what a delete would remove, resolved before the user confirms it.
type DeletePlan struct {
	Contexts       []string
	OrphanClusters []string
	OrphanUsers    []string
	KeptClusters   []string // referenced by contexts that survive
	KeptUsers      []string
	ClearsCurrent  bool
	Files          []string // files that will be rewritten
}

// PlanDelete resolves which clusters and users become unreferenced once the
// named contexts are gone. A cluster or user is only an orphan if no surviving
// context points at it.
func (s *Store) PlanDelete(names []string) DeletePlan {
	doomed := map[string]bool{}
	for _, n := range names {
		doomed[n] = true
	}

	survivingClusters := map[string]bool{}
	survivingUsers := map[string]bool{}
	for name, c := range s.cfg.Contexts {
		if doomed[name] {
			continue
		}
		survivingClusters[c.Cluster] = true
		survivingUsers[c.AuthInfo] = true
	}

	plan := DeletePlan{Contexts: append([]string(nil), names...)}
	sort.Strings(plan.Contexts)

	seenC, seenU := map[string]bool{}, map[string]bool{}
	for _, n := range names {
		c, ok := s.cfg.Contexts[n]
		if !ok {
			continue
		}
		if c.Cluster != "" && !seenC[c.Cluster] {
			seenC[c.Cluster] = true
			if survivingClusters[c.Cluster] {
				plan.KeptClusters = append(plan.KeptClusters, c.Cluster)
			} else {
				plan.OrphanClusters = append(plan.OrphanClusters, c.Cluster)
			}
		}
		if c.AuthInfo != "" && !seenU[c.AuthInfo] {
			seenU[c.AuthInfo] = true
			if survivingUsers[c.AuthInfo] {
				plan.KeptUsers = append(plan.KeptUsers, c.AuthInfo)
			} else {
				plan.OrphanUsers = append(plan.OrphanUsers, c.AuthInfo)
			}
		}
		if s.cfg.CurrentContext == n {
			plan.ClearsCurrent = true
		}
	}
	sort.Strings(plan.OrphanClusters)
	sort.Strings(plan.OrphanUsers)
	sort.Strings(plan.KeptClusters)
	sort.Strings(plan.KeptUsers)
	plan.Files = s.Files()
	return plan
}

// Apply executes a plan produced by PlanDelete.
func (s *Store) Apply(plan DeletePlan) error {
	if err := s.backup(); err != nil {
		return err
	}
	for _, n := range plan.Contexts {
		delete(s.cfg.Contexts, n)
		if s.cfg.CurrentContext == n {
			s.cfg.CurrentContext = ""
		}
	}
	for _, c := range plan.OrphanClusters {
		delete(s.cfg.Clusters, c)
	}
	for _, u := range plan.OrphanUsers {
		delete(s.cfg.AuthInfos, u)
	}
	return s.write()
}

func (s *Store) Rename(old, new string) error {
	if old == new {
		return nil
	}
	if new == "" {
		return fmt.Errorf("name cannot be empty")
	}
	if _, exists := s.cfg.Contexts[new]; exists {
		return fmt.Errorf("context %q already exists", new)
	}
	c, ok := s.cfg.Contexts[old]
	if !ok {
		return fmt.Errorf("context %q not found", old)
	}
	if err := s.backup(); err != nil {
		return err
	}
	s.cfg.Contexts[new] = c
	delete(s.cfg.Contexts, old)
	if s.cfg.CurrentContext == old {
		s.cfg.CurrentContext = new
	}
	return s.write()
}

func (s *Store) SetCurrent(name string) error {
	if _, ok := s.cfg.Contexts[name]; !ok {
		return fmt.Errorf("context %q not found", name)
	}
	if s.cfg.CurrentContext == name {
		return nil
	}
	if err := s.backup(); err != nil {
		return err
	}
	s.cfg.CurrentContext = name
	return s.write()
}

func (s *Store) SetNamespace(ctxName, ns string) error {
	c, ok := s.cfg.Contexts[ctxName]
	if !ok {
		return fmt.Errorf("context %q not found", ctxName)
	}
	if c.Namespace == ns {
		return nil
	}
	if err := s.backup(); err != nil {
		return err
	}
	c.Namespace = ns
	return s.write()
}

func (s *Store) write() error {
	// relativizePaths=true matches kubectl's own behaviour: paths already
	// written relative to the config file stay relative.
	if err := clientcmd.ModifyConfig(s.pathOptions, *s.cfg, true); err != nil {
		return fmt.Errorf("writing kubeconfig: %w", err)
	}
	return s.Reload()
}

// backup snapshots every kubeconfig file in the loading chain into a timestamped
// directory before the first mutation of a write.
func (s *Store) backup() error {
	if !s.Backups {
		return nil
	}
	files := s.Files()
	if len(files) == 0 {
		return nil
	}
	stamp := time.Now().Format("20060102-150405")
	dir := filepath.Join(s.BackupDir, stamp)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating backup dir: %w", err)
	}
	for i, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("backing up %s: %w", f, err)
		}
		// Prefix with the precedence index so a merged chain with colliding
		// basenames does not overwrite itself inside one snapshot.
		name := fmt.Sprintf("%02d-%s", i, filepath.Base(f))
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			return fmt.Errorf("backing up %s: %w", f, err)
		}
	}
	s.pruneBackups()
	return nil
}

func (s *Store) pruneBackups() {
	if s.KeepBackups <= 0 {
		return
	}
	entries, err := os.ReadDir(s.BackupDir)
	if err != nil {
		return
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	if len(dirs) <= s.KeepBackups {
		return
	}
	sort.Strings(dirs) // timestamp names sort chronologically
	for _, old := range dirs[:len(dirs)-s.KeepBackups] {
		os.RemoveAll(filepath.Join(s.BackupDir, old))
	}
}

func (s *Store) LastBackupDir() string {
	entries, err := os.ReadDir(s.BackupDir)
	if err != nil {
		return ""
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	if len(dirs) == 0 {
		return ""
	}
	sort.Strings(dirs)
	return filepath.Join(s.BackupDir, dirs[len(dirs)-1])
}
