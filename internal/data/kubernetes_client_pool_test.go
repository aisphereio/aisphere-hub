package data

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/aisphereio/aisphere-hub/internal/biz"
	"github.com/aisphereio/kernel/kubernetesx"
)

// fakeClient is a minimal kubernetesx.Client stand-in. It embeds the interface
// (nil) so the struct satisfies kubernetesx.Client without implementing every
// method; only Probe and Apply are overridden because those are the only paths
// the pool tests exercise. Calling an unimplemented method panics, which is
// the desired failure mode for a test that drifts off the tested path.
type fakeClient struct {
	kubernetesx.Client // embedded nil; unimplemented methods panic
	id                 int
	probeMu            sync.Mutex
	probeCnt           int
}

func (f *fakeClient) Probe(ctx context.Context, req kubernetesx.ProbeRequest) (kubernetesx.ProbeResult, error) {
	f.probeMu.Lock()
	f.probeCnt++
	f.probeMu.Unlock()
	return kubernetesx.ProbeResult{ServerVersion: kubernetesx.VersionInfo{GitVersion: "fake-" + itoa(f.id)}}, nil
}

// itoa avoids strconv import for a tiny helper.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// fakeStore is a biz.ClusterCredentialStore stand-in used by the pool when the
// caller does not supply a credential (ApplyNamespace/DeleteNamespace paths).
// It returns whatever was Put, keyed by ref.
type fakeStore struct {
	mu   sync.Mutex
	rows map[string]kubernetesx.Credential
	n    int
}

func newFakeStore() *fakeStore { return &fakeStore{rows: map[string]kubernetesx.Credential{}} }

func (s *fakeStore) NewCredentialRef() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	return "ref-" + itoa(s.n), nil
}

func (s *fakeStore) Put(ctx context.Context, clusterID string, rev int64, value kubernetesx.Credential) (biz.CredentialLocator, error) {
	ref, err := s.NewCredentialRef()
	if err != nil {
		return biz.CredentialLocator{}, err
	}
	return s.PutWithRef(ctx, clusterID, ref, rev, value)
}

func (s *fakeStore) PutWithRef(_ context.Context, clusterID, ref string, rev int64, value kubernetesx.Credential) (biz.CredentialLocator, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[ref] = value
	return biz.CredentialLocator{ClusterID: clusterID, CredentialRef: ref, CredentialRevision: rev}, nil
}

func (s *fakeStore) Get(_ context.Context, locator biz.CredentialLocator) (kubernetesx.Credential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.rows[locator.CredentialRef]
	if !ok {
		return kubernetesx.Credential{}, errors.New("not found")
	}
	return v, nil
}

func (s *fakeStore) Delete(_ context.Context, ref string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.rows, ref)
	return nil
}

func (s *fakeStore) RotateKey(_ context.Context, from, to string) (int, error) { return 0, nil }

// newTestPool builds a pool with an instrumented factory: each build increments
// the counter and returns a distinct fakeClient. Tests assert the counter to
// prove cache hits / misses / singleflight dedup.
func newTestPool(t *testing.T, store biz.ClusterCredentialStore) (*k8sClientPool, *int32) {
	t.Helper()
	var builds int32
	p := &k8sClientPool{
		store:  store,
		policy: nil, // factory bypasses policy for fake credentials
		factory: func(_ context.Context, clusterID string, _ biz.CredentialLocator, _ kubernetesx.Credential) (kubernetesx.Client, error) {
			n := atomic.AddInt32(&builds, 1)
			return &fakeClient{id: int(n)}, nil
		},
	}
	return p, &builds
}

func TestPool_CacheHit(t *testing.T) {
	p, builds := newTestPool(t, newFakeStore())
	loc := biz.CredentialLocator{ClusterID: "c1", CredentialRef: "r1", CredentialRevision: 1}
	cred := kubernetesx.Credential{Kind: kubernetesx.CredentialKindServiceAccount, Host: "https://10.0.0.1:6443", Token: "t"}

	if _, err := p.Probe(context.Background(), "c1", loc, cred); err != nil {
		t.Fatalf("first Probe error = %v", err)
	}
	if _, err := p.Probe(context.Background(), "c1", loc, cred); err != nil {
		t.Fatalf("second Probe error = %v", err)
	}
	if got := atomic.LoadInt32(builds); got != 1 {
		t.Fatalf("builds after two Probes = %d, want 1 (cache hit)", got)
	}
}

func TestPool_InvalidateOnRotate(t *testing.T) {
	p, builds := newTestPool(t, newFakeStore())
	cred := kubernetesx.Credential{Kind: kubernetesx.CredentialKindServiceAccount, Host: "https://10.0.0.1:6443", Token: "t"}

	loc1 := biz.CredentialLocator{ClusterID: "c1", CredentialRef: "r1", CredentialRevision: 1}
	if _, err := p.Probe(context.Background(), "c1", loc1, cred); err != nil {
		t.Fatalf("Probe rev1 error = %v", err)
	}

	// Rotate: revision bumps to 2. The cache entry (rev 1) must not be reused.
	loc2 := biz.CredentialLocator{ClusterID: "c1", CredentialRef: "r2", CredentialRevision: 2}
	if _, err := p.Probe(context.Background(), "c1", loc2, cred); err != nil {
		t.Fatalf("Probe rev2 error = %v", err)
	}
	if got := atomic.LoadInt32(builds); got != 2 {
		t.Fatalf("builds after rotate = %d, want 2 (revision mismatch refetches)", got)
	}
	// Third Probe at rev2 should hit the new cache entry.
	if _, err := p.Probe(context.Background(), "c1", loc2, cred); err != nil {
		t.Fatalf("Probe rev2 again error = %v", err)
	}
	if got := atomic.LoadInt32(builds); got != 2 {
		t.Fatalf("builds after rev2 repeat = %d, want 2 (new cache hit)", got)
	}
}

func TestPool_ConcurrentSingleflight(t *testing.T) {
	p, builds := newTestPool(t, newFakeStore())
	loc := biz.CredentialLocator{ClusterID: "c1", CredentialRef: "r1", CredentialRevision: 1}
	cred := kubernetesx.Credential{Kind: kubernetesx.CredentialKindServiceAccount, Host: "https://10.0.0.1:6443", Token: "t"}

	// Make the factory slow so concurrent callers overlap inside singleflight.
	p.factory = func(_ context.Context, _ string, _ biz.CredentialLocator, _ kubernetesx.Credential) (kubernetesx.Client, error) {
		atomic.AddInt32(builds, 1)
		time.Sleep(20 * time.Millisecond)
		return &fakeClient{id: 1}, nil
	}

	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			_, errs[i] = p.Probe(context.Background(), "c1", loc, cred)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d error = %v", i, err)
		}
	}
	if got := atomic.LoadInt32(builds); got != 1 {
		t.Fatalf("builds after %d concurrent Probes = %d, want 1 (singleflight dedup)", n, got)
	}
}

func TestPool_RevisionMismatchRefetches(t *testing.T) {
	p, builds := newTestPool(t, newFakeStore())
	cred := kubernetesx.Credential{Kind: kubernetesx.CredentialKindServiceAccount, Host: "https://10.0.0.1:6443", Token: "t"}

	// Seed cache at rev 5.
	loc5 := biz.CredentialLocator{ClusterID: "c1", CredentialRef: "r5", CredentialRevision: 5}
	if _, err := p.Probe(context.Background(), "c1", loc5, cred); err != nil {
		t.Fatalf("seed Probe error = %v", err)
	}
	// Request with rev 3 (older). Must not return the rev-5 cached client.
	loc3 := biz.CredentialLocator{ClusterID: "c1", CredentialRef: "r3", CredentialRevision: 3}
	if _, err := p.Probe(context.Background(), "c1", loc3, cred); err != nil {
		t.Fatalf("rev3 Probe error = %v", err)
	}
	if got := atomic.LoadInt32(builds); got != 2 {
		t.Fatalf("builds after older revision = %d, want 2 (mismatch refetches)", got)
	}
}

func TestPool_InvalidateClusterDropsEntry(t *testing.T) {
	p, builds := newTestPool(t, newFakeStore())
	loc := biz.CredentialLocator{ClusterID: "c1", CredentialRef: "r1", CredentialRevision: 1}
	cred := kubernetesx.Credential{Kind: kubernetesx.CredentialKindServiceAccount, Host: "https://10.0.0.1:6443", Token: "t"}

	if _, err := p.Probe(context.Background(), "c1", loc, cred); err != nil {
		t.Fatalf("first Probe error = %v", err)
	}
	p.InvalidateCluster(context.Background(), "c1")
	if _, err := p.Probe(context.Background(), "c1", loc, cred); err != nil {
		t.Fatalf("post-invalidate Probe error = %v", err)
	}
	if got := atomic.LoadInt32(builds); got != 2 {
		t.Fatalf("builds after invalidate = %d, want 2 (cache dropped)", got)
	}
}

func TestPool_CloseClearsCache(t *testing.T) {
	p, builds := newTestPool(t, newFakeStore())
	loc := biz.CredentialLocator{ClusterID: "c1", CredentialRef: "r1", CredentialRevision: 1}
	cred := kubernetesx.Credential{Kind: kubernetesx.CredentialKindServiceAccount, Host: "https://10.0.0.1:6443", Token: "t"}

	if _, err := p.Probe(context.Background(), "c1", loc, cred); err != nil {
		t.Fatalf("Probe error = %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("Close error = %v", err)
	}
	if _, err := p.Probe(context.Background(), "c1", loc, cred); err != nil {
		t.Fatalf("post-close Probe error = %v", err)
	}
	if got := atomic.LoadInt32(builds); got != 2 {
		t.Fatalf("builds after Close = %d, want 2 (Close cleared cache)", got)
	}
}

// applySpyClient overrides Apply to record the seen credential (via a factory
// closure) and return nil, so the ApplyNamespace path completes without a real
// controller-runtime client.
type applySpyClient struct {
	kubernetesx.Client
}

func (applySpyClient) Apply(_ context.Context, _ ctrlclient.Object, _ kubernetesx.ApplyOptions) error {
	return nil
}

func TestPool_LoadsCredentialFromStoreWhenCallerOmits(t *testing.T) {
	// ApplyNamespace path: caller passes zero Credential (Kind=""), so the
	// pool must fetch from the store. We verify the factory saw the loaded
	// credential.
	store := newFakeStore()
	cred := kubernetesx.Credential{Kind: kubernetesx.CredentialKindServiceAccount, Host: "https://10.0.0.1:6443", Token: "t"}
	loc, err := store.Put(context.Background(), "c1", 1, cred)
	if err != nil {
		t.Fatal(err)
	}

	var seen kubernetesx.Credential
	var seenMu sync.Mutex
	p := &k8sClientPool{
		store:  store,
		policy: nil,
		factory: func(_ context.Context, _ string, _ biz.CredentialLocator, c kubernetesx.Credential) (kubernetesx.Client, error) {
			seenMu.Lock()
			seen = c
			seenMu.Unlock()
			return applySpyClient{}, nil
		},
	}
	if err := p.ApplyNamespace(context.Background(), "c1", loc, biz.NamespaceApplySpec{Name: "ns1"}); err != nil {
		t.Fatalf("ApplyNamespace error = %v (factory should have run; Apply stub returns nil)", err)
	}
	seenMu.Lock()
	defer seenMu.Unlock()
	if seen.Kind != kubernetesx.CredentialKindServiceAccount {
		t.Fatalf("credential loaded from store = %+v, want ServiceAccount (caller omitted cred)", seen)
	}
	if seen.Token != "t" {
		t.Fatalf("credential token = %q, want t", seen.Token)
	}
}

func TestPool_StoreErrorPropagates(t *testing.T) {
	// A failing store + omitted credential must surface the store error, not
	// build a client.
	failStore := &failingStore{}
	p := &k8sClientPool{
		store:  failStore,
		policy: nil,
		factory: func(context.Context, string, biz.CredentialLocator, kubernetesx.Credential) (kubernetesx.Client, error) {
			t.Fatal("factory called despite store failure")
			return nil, nil
		},
	}
	loc := biz.CredentialLocator{ClusterID: "c1", CredentialRef: "r1", CredentialRevision: 1}
	err := p.ApplyNamespace(context.Background(), "c1", loc, biz.NamespaceApplySpec{Name: "ns1"})
	if err == nil {
		t.Fatal("ApplyNamespace with failing store: want error, got nil")
	}
}

// failingStore always errors on Get.
type failingStore struct{}

func (failingStore) NewCredentialRef() (string, error)                      { return "ref-fail", nil }
func (failingStore) Put(context.Context, string, int64, kubernetesx.Credential) (biz.CredentialLocator, error) {
	return biz.CredentialLocator{}, nil
}
func (failingStore) PutWithRef(_ context.Context, clusterID, ref string, rev int64, _ kubernetesx.Credential) (biz.CredentialLocator, error) {
	return biz.CredentialLocator{ClusterID: clusterID, CredentialRef: ref, CredentialRevision: rev}, nil
}
func (failingStore) Get(context.Context, biz.CredentialLocator) (kubernetesx.Credential, error) {
	return kubernetesx.Credential{}, errors.New("store unavailable")
}
func (failingStore) Delete(context.Context, string) error                  { return nil }
func (failingStore) RotateKey(context.Context, string, string) (int, error) { return 0, nil }
