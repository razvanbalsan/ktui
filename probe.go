package main

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

type ProbeState int

const (
	ProbeUnknown ProbeState = iota
	ProbePending
	ProbeLive        // API server answered and our credentials were accepted
	ProbeAuthExpired // reachable, credentials rejected (401)
	ProbeCredError   // could not even build credentials (exec plugin missing/failed)
	ProbeTLSError    // reachable, certificate not trusted
	ProbeUnreachable // DNS/connect/timeout
)

func (p ProbeState) Label() string {
	switch p {
	case ProbePending:
		return "checking"
	case ProbeLive:
		return "live"
	case ProbeAuthExpired:
		return "auth expired"
	case ProbeCredError:
		return "cred error"
	case ProbeTLSError:
		return "tls error"
	case ProbeUnreachable:
		return "unreachable"
	}
	return "unknown"
}

type ProbeResult struct {
	Context string
	State   ProbeState
	Detail  string
	Latency time.Duration
}

// clientFor builds a client for one named context out of the same file chain
// the Store reads, with exec credential plugins pinned non-interactive so a
// plugin can never grab the terminal out from under the TUI.
func clientFor(ctxName string, timeout time.Duration) (*kubernetes.Clientset, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		rules,
		&clientcmd.ConfigOverrides{CurrentContext: ctxName},
	)
	rc, err := cc.ClientConfig()
	if err != nil {
		return nil, err
	}
	rc.Timeout = timeout
	if rc.ExecProvider != nil {
		rc.ExecProvider.InteractiveMode = clientcmdapi.NeverExecInteractiveMode
	}
	rc.UserAgent = rest.DefaultKubernetesUserAgent() + " ktui"
	return kubernetes.NewForConfig(rc)
}

// Probe classifies one context.
//
// It issues a single namespace list capped at one item. The response code is
// what carries the signal: 200 means the credentials were accepted, 403 means
// they were accepted and RBAC declined the verb (still a working context), and
// 401 means they were rejected — the case worth surfacing as "auth expired".
func Probe(ctx context.Context, ctxName string, timeout time.Duration) ProbeResult {
	res := ProbeResult{Context: ctxName}
	start := time.Now()

	cs, err := clientFor(ctxName, timeout)
	if err != nil {
		res.State = ProbeCredError
		res.Detail = firstLine(err.Error())
		res.Latency = time.Since(start)
		return res
	}

	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	_, err = cs.CoreV1().Namespaces().List(cctx, metav1.ListOptions{Limit: 1})
	res.Latency = time.Since(start)

	if err == nil {
		res.State = ProbeLive
		return res
	}

	res.Detail = firstLine(err.Error())
	res.State = classify(err)
	return res
}

func classify(err error) ProbeState {
	switch {
	case apierrors.IsUnauthorized(err):
		return ProbeAuthExpired
	case apierrors.IsForbidden(err):
		// Authenticated, just not permitted to list namespaces.
		return ProbeLive
	}

	msg := err.Error()

	// Exec credential plugins fail before any network I/O happens.
	if strings.Contains(msg, "exec plugin") ||
		strings.Contains(msg, "getting credentials") ||
		strings.Contains(msg, "executable file not found") ||
		strings.Contains(msg, "no Auth Provider found") {
		return ProbeCredError
	}

	if strings.Contains(msg, "x509") ||
		strings.Contains(msg, "certificate") ||
		strings.Contains(msg, "tls: ") {
		return ProbeTLSError
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return ProbeUnreachable
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Timeout() {
		return ProbeUnreachable
	}
	if errors.Is(err, context.DeadlineExceeded) ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "no such host") ||
		strings.Contains(msg, "i/o timeout") ||
		strings.Contains(msg, "deadline exceeded") ||
		strings.Contains(msg, "network is unreachable") ||
		strings.Contains(msg, "no route to host") ||
		strings.Contains(msg, "EOF") {
		return ProbeUnreachable
	}

	return ProbeUnknown
}

// ListNamespaces fetches the namespaces of one context for the drill-in picker.
func ListNamespaces(ctx context.Context, ctxName string, timeout time.Duration) ([]string, error) {
	cs, err := clientFor(ctxName, timeout)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	list, err := cs.CoreV1().Namespaces().List(cctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(list.Items))
	for _, ns := range list.Items {
		out = append(out, ns.Name)
	}
	return out, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 160 {
		s = s[:157] + "..."
	}
	return s
}
