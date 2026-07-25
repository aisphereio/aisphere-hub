package biz

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/aisphereio/kernel/logx"
)

// recordingSyncer is a sandboxSyncer stub that records the call order per
// namespace and can inject errors per core. It lets the reconciler test
// assert the Claims → WarmPools → Sandboxes ordering without a full
// SandboxUsecase wiring.
type recordingSyncer struct {
	mu      sync.Mutex
	calls   []string // "claims:<ns>", "warmpools:<ns>", "sandboxes:<ns>"
	claimsErr, warmErr, sandErr error
}

func (s *recordingSyncer) syncSandboxClaimsCore(_ context.Context, namespaceID string) (int, int, int, error) {
	s.mu.Lock()
	s.calls = append(s.calls, "claims:"+namespaceID)
	err := s.claimsErr
	s.mu.Unlock()
	return 0, 0, 0, err
}

func (s *recordingSyncer) syncWarmPoolsCore(_ context.Context, namespaceID string) (int, int, error) {
	s.mu.Lock()
	s.calls = append(s.calls, "warmpools:"+namespaceID)
	err := s.warmErr
	s.mu.Unlock()
	return 0, 0, err
}

func (s *recordingSyncer) syncSandboxesCore(_ context.Context, namespaceID string) (int, int, int, error) {
	s.mu.Lock()
	s.calls = append(s.calls, "sandboxes:"+namespaceID)
	err := s.sandErr
	s.mu.Unlock()
	return 0, 0, 0, err
}

func (s *recordingSyncer) ordered() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.calls))
	copy(out, s.calls)
	return out
}

// verify *SandboxUsecase satisfies sandboxSyncer at compile time. The usecase
// methods are unexported, so this assertion lives in the same package.
var _ sandboxSyncer = (*SandboxUsecase)(nil)

// TestSandboxReconcilerOrdering asserts the per-namespace core invocation order
// is Claims → WarmPools → Sandboxes, so a claim-allocated sandbox gets its Hub
// row (owner = claim owner) before SyncSandboxes would otherwise import it as
// ns-owner.
func TestSandboxReconcilerOrdering(t *testing.T) {
	repo := newFakeNamespaceRepo()
	// Two READY namespaces with a remote UID — both are reconcile candidates.
	if _, err := repo.CreateNamespace(context.Background(), &Namespace{
		ID: "ns-a", ClusterID: "c1", KubeName: "a",
		OwnerType: "user", OwnerID: "u1", Revision: 1,
		Lifecycle: NamespaceLifecycleReady, KubernetesUID: "uid-a",
		Visibility: NamespaceVisibilityPrivate,
	}); err != nil {
		t.Fatalf("create ns-a: %v", err)
	}
	if _, err := repo.CreateNamespace(context.Background(), &Namespace{
		ID: "ns-b", ClusterID: "c1", KubeName: "b",
		OwnerType: "user", OwnerID: "u2", Revision: 1,
		Lifecycle: NamespaceLifecycleReady, KubernetesUID: "uid-b",
		Visibility: NamespaceVisibilityPrivate,
	}); err != nil {
		t.Fatalf("create ns-b: %v", err)
	}

	syncer := &recordingSyncer{}
	r := NewSandboxReconciler(syncer, repo, logx.Noop(), 10)

	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := syncer.ordered()
	// The fake map iteration order is non-deterministic, so assert per-namespace
	// ordering rather than global. Group calls by namespace id (strip the
	// "claims:"/"warmpools:"/"sandboxes:" prefix).
	byNS := map[string][]string{}
	for _, c := range got {
		for _, prefix := range []string{"claims:", "warmpools:", "sandboxes:"} {
			if ns, ok := strings.CutPrefix(c, prefix); ok {
				byNS[ns] = append(byNS[ns], c)
				break
			}
		}
	}
	// Each namespace must see claims before warmpools before sandboxes.
	for _, ns := range []string{"ns-a", "ns-b"} {
		seq := []string{"claims:" + ns, "warmpools:" + ns, "sandboxes:" + ns}
		gotNS := byNS[ns]
		if len(gotNS) != 3 {
			t.Fatalf("ns %s: expected 3 calls, got %d (%v)", ns, len(gotNS), gotNS)
		}
		for i, want := range seq {
			if gotNS[i] != want {
				t.Errorf("ns %s call %d: got %q, want %q", ns, i, gotNS[i], want)
			}
		}
	}

	// Fair round-robin: both namespaces had last_sync_at stamped, even though
	// no core errored. This guards against a future regression where a failing
	// namespace monopolizes the scan.
	for _, ns := range []string{"ns-a", "ns-b"} {
		if r2, ok := repo.namespaces[ns]; !ok || r2.LastSyncAt.IsZero() {
			t.Errorf("ns %s: last_sync_at not stamped", ns)
		}
	}
}

// TestSandboxReconcilerErrorIsolation asserts a failing namespace is logged,
// counted as failed, but does not abort the pass — and is still stamped so it
// does not monopolize the next round.
func TestSandboxReconcilerErrorIsolation(t *testing.T) {
	repo := newFakeNamespaceRepo()
	if _, err := repo.CreateNamespace(context.Background(), &Namespace{
		ID: "ns-err", ClusterID: "c1", KubeName: "err",
		OwnerType: "user", OwnerID: "u1", Revision: 1,
		Lifecycle: NamespaceLifecycleReady, KubernetesUID: "uid-err",
		Visibility: NamespaceVisibilityPrivate,
	}); err != nil {
		t.Fatalf("create ns-err: %v", err)
	}

	syncer := &recordingSyncer{claimsErr: errors.New("boom")}
	r := NewSandboxReconciler(syncer, repo, logx.Noop(), 10)

	// Run must return nil (per-namespace failures are not infra errors).
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run should return nil on per-namespace failure, got %v", err)
	}

	// The claims error short-circuits convergeOne before warmpools/sandboxes.
	got := syncer.ordered()
	if len(got) != 1 || got[0] != "claims:ns-err" {
		t.Errorf("expected single claims call on error, got %v", got)
	}
	// Still stamped — failing namespace must not monopolize the next pass.
	if ns, ok := repo.namespaces["ns-err"]; !ok || ns.LastSyncAt.IsZero() {
		t.Errorf("failing namespace last_sync_at not stamped")
	}
}

// TestSandboxReconcilerSkipsUnreadyNamespaces asserts only READY namespaces
// with a populated KubernetesUID are converged (a CREATING namespace has no
// remote CRD to sync yet).
func TestSandboxReconcilerSkipsUnreadyNamespaces(t *testing.T) {
	repo := newFakeNamespaceRepo()
	// READY + UID → candidate.
	if _, err := repo.CreateNamespace(context.Background(), &Namespace{
		ID: "ns-ready", ClusterID: "c1", KubeName: "ready",
		OwnerType: "user", OwnerID: "u1", Revision: 1,
		Lifecycle: NamespaceLifecycleReady, KubernetesUID: "uid-ready",
		Visibility: NamespaceVisibilityPrivate,
	}); err != nil {
		t.Fatalf("create ns-ready: %v", err)
	}
	// CREATING + no UID → not a candidate (no remote CRD yet).
	if _, err := repo.CreateNamespace(context.Background(), &Namespace{
		ID: "ns-creating", ClusterID: "c1", KubeName: "creating",
		OwnerType: "user", OwnerID: "u2", Revision: 1,
		Lifecycle: NamespaceLifecycleCreating, KubernetesUID: "",
		Visibility: NamespaceVisibilityPrivate,
	}); err != nil {
		t.Fatalf("create ns-creating: %v", err)
	}

	syncer := &recordingSyncer{}
	r := NewSandboxReconciler(syncer, repo, logx.Noop(), 10)

	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := syncer.ordered()
	if len(got) != 3 { // only ns-ready's triple
		t.Errorf("expected 3 calls (ns-ready triple only), got %d (%v)", len(got), got)
	}
	for _, c := range got {
		if c != "claims:ns-ready" && c != "warmpools:ns-ready" && c != "sandboxes:ns-ready" {
			t.Errorf("unexpected call for non-candidate: %q", c)
		}
	}
}

