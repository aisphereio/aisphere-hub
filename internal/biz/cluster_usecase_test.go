package biz

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aisphereio/kernel/authn"
	"github.com/aisphereio/kernel/kubernetesx"
	"github.com/aisphereio/kernel/logx"
)

// fakeClusterRepo is a biz.ClusterRepository stand-in. It stores clusters in
// a map and records CAS / status calls so tests assert compensation behavior.
type fakeClusterRepo struct {
	mu           sync.Mutex
	clusters     map[string]*Cluster
	createErr    error
	statusCalls  []statusCall
	credCalls    []credCall
	softDeleted  map[string]bool
	nsCount      map[string]int64
	credCASFail  bool // when true, UpdateClusterCredential always returns ErrClusterRevisionConflict
}

type statusCall struct {
	id, expected, next string
}
type credCall struct {
	id       string
	expected int64
	newRev   int64
	newRef   string
}

func newFakeClusterRepo() *fakeClusterRepo {
	return &fakeClusterRepo{
		clusters:    map[string]*Cluster{},
		softDeleted: map[string]bool{},
		nsCount:     map[string]int64{},
	}
}

func (r *fakeClusterRepo) CreateCluster(_ context.Context, c *Cluster) (*Cluster, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.createErr != nil {
		return nil, r.createErr
	}
	stored := *c
	r.clusters[c.ID] = &stored
	return &stored, nil
}
func (r *fakeClusterRepo) GetCluster(_ context.Context, id string) (*Cluster, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.clusters[id]
	if !ok || r.softDeleted[id] {
		return nil, ErrClusterNotFound
	}
	stored := *c
	return &stored, nil
}
func (r *fakeClusterRepo) GetClusterByOrgName(_ context.Context, orgID, name string) (*Cluster, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.clusters {
		if c.OrgID == orgID && c.Name == name && !r.softDeleted[c.ID] {
			stored := *c
			return &stored, nil
		}
	}
	return nil, ErrClusterNotFound
}
func (r *fakeClusterRepo) ListClusterCandidates(_ context.Context, orgID, cursor string, maxScan int) ([]*Cluster, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []*Cluster{}
	for _, c := range r.clusters {
		if c.OrgID == orgID && !r.softDeleted[c.ID] {
			stored := *c
			out = append(out, &stored)
		}
	}
	return out, "", nil
}
func (r *fakeClusterRepo) ListClustersByOrg(_ context.Context, orgID string) ([]*Cluster, error) {
	candidates, _, _ := r.ListClusterCandidates(context.Background(), orgID, "", 0)
	return candidates, nil
}
func (r *fakeClusterRepo) UpdateClusterWithCAS(_ context.Context, id string, expected int64, updates map[string]any) (*Cluster, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.clusters[id]
	if !ok || c.Revision != expected {
		return nil, ErrClusterRevisionConflict
	}
	c.Revision++
	return &(*c), nil
}
func (r *fakeClusterRepo) UpdateClusterStatus(_ context.Context, id, expected, next string, extra map[string]any) (*Cluster, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.clusters[id]
	if !ok {
		return nil, ErrClusterNotFound
	}
	r.statusCalls = append(r.statusCalls, statusCall{id, expected, next})
	if expected != "" && c.Status != expected {
		return nil, ErrClusterNotFound
	}
	c.Status = next
	if v, ok := extra["cluster_uid"]; ok {
		c.ClusterUID = v.(string)
	}
	if v, ok := extra["kubernetes_version"]; ok {
		c.KubernetesVersion = v.(string)
	}
	return &(*c), nil
}
func (r *fakeClusterRepo) UpdateClusterCredential(_ context.Context, id string, expected, newRev int64, newRef string) (*Cluster, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Record the attempt regardless of outcome so tests can assert CAS was tried.
	r.credCalls = append(r.credCalls, credCall{id, expected, newRev, newRef})
	c, ok := r.clusters[id]
	if !ok || c.Revision != expected || r.credCASFail {
		return nil, ErrClusterRevisionConflict
	}
	c.CredentialRef = newRef
	c.CredentialRevision = newRev
	c.Revision++
	return &(*c), nil
}
func (r *fakeClusterRepo) SoftDeleteCluster(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.clusters[id]; !ok {
		return ErrClusterNotFound
	}
	r.softDeleted[id] = true
	r.clusters[id].Status = ClusterStatusDeleted
	return nil
}
func (r *fakeClusterRepo) CountNamespacesForCluster(_ context.Context, clusterID string) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.nsCount[clusterID], nil
}

// fakeCredStore is a biz.ClusterCredentialStore stand-in. Put records refs so
// tests assert compensate Delete; Get returns whatever was Put; Delete removes.
type fakeCredStore struct {
	mu        sync.Mutex
	putCalls  int
	deletes   []string
	rows      map[string]kubernetesx.Credential
	putErr    error
}

func newFakeCredStore() *fakeCredStore {
	return &fakeCredStore{rows: map[string]kubernetesx.Credential{}}
}

func (s *fakeCredStore) Put(_ context.Context, clusterID string, rev int64, value kubernetesx.Credential) (CredentialLocator, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.putErr != nil {
		return CredentialLocator{}, s.putErr
	}
	s.putCalls++
	ref := "ref-" + clusterID + "-rev" + revString(rev)
	s.rows[ref] = value
	return CredentialLocator{ClusterID: clusterID, CredentialRef: ref, CredentialRevision: rev}, nil
}
func (s *fakeCredStore) Get(_ context.Context, loc CredentialLocator) (kubernetesx.Credential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.rows[loc.CredentialRef]
	if !ok {
		return kubernetesx.Credential{}, errors.New("not found")
	}
	return v, nil
}
func (s *fakeCredStore) Delete(_ context.Context, ref string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deletes = append(s.deletes, ref)
	delete(s.rows, ref)
	return nil
}
func (s *fakeCredStore) RotateKey(_ context.Context, from, to string) (int, error) { return 0, nil }

// fakeEndpoint is a biz.EndpointPolicy that approves everything (tests cover
// the SSRF guard in internal/data, not here).
type fakeEndpoint struct{ fail bool }

func (f fakeEndpoint) Validate(_ context.Context, _ string) (ResolvedEndpoint, error) {
	if f.fail {
		return ResolvedEndpoint{}, ErrClusterInvalidArgument
	}
	return ResolvedEndpoint{OriginalHost: "10.0.0.1", ResolvedIPs: []string{"10.0.0.1"}}, nil
}

// fakeProvider is a biz.KubernetesProvider stand-in. Probe returns a
// configurable result/error so RotateCredential UID-mismatch + CreateCluster
// probe-failure paths are testable.
type fakeProvider struct {
	probeResult kubernetesx.ProbeResult
	probeErr    error
	uidOverride string // when non-empty, overrides probeResult.ClusterUID
	invalidate  []string
}

func (p *fakeProvider) Probe(_ context.Context, _ string, _ CredentialLocator, _ kubernetesx.Credential) (kubernetesx.ProbeResult, error) {
	if p.probeErr != nil {
		return kubernetesx.ProbeResult{}, p.probeErr
	}
	r := p.probeResult
	if p.uidOverride != "" {
		r.ClusterUID = p.uidOverride
	}
	return r, nil
}
func (p *fakeProvider) ApplyNamespace(context.Context, string, CredentialLocator, NamespaceApplySpec) error {
	return nil
}
func (p *fakeProvider) DeleteNamespace(context.Context, string, CredentialLocator, string) error { return nil }
func (p *fakeProvider) ListNamespaces(context.Context, string, CredentialLocator) ([]NamespaceSyncResult, error) {
	return nil, nil
}
func (p *fakeProvider) InvalidateCluster(_ context.Context, clusterID string) {
	p.invalidate = append(p.invalidate, clusterID)
}

// fakeOutbox records Enqueue calls.
type fakeOutbox struct {
	mu     sync.Mutex
	events []outboxEvent
}
type outboxEvent struct{ aggType, aggID, eventType string }

func (o *fakeOutbox) Enqueue(_ context.Context, aggType, aggID, eventType string, _ map[string]any) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, outboxEvent{aggType, aggID, eventType})
	return nil
}

// fakeClusterRels is a biz.ClusterRelationships stand-in. WriteRelationships
// records tuples; Check returns allowAll; BatchCheck returns allowAll per check.
type fakeClusterRels struct {
	mu        sync.Mutex
	written   []AuthzRelationship
	writeErr  error
	revoked   []AuthzObjectRef
	allowAll  bool
	checkOver map[string]bool // permission -> allowed (overrides allowAll)
}

func newFakeClusterRels() *fakeClusterRels { return &fakeClusterRels{allowAll: true, checkOver: map[string]bool{}} }

func (r *fakeClusterRels) Check(_ context.Context, req AuthzCheckRequest) (AuthzDecision, error) {
	if allow, ok := r.checkOver[req.Permission]; ok {
		return AuthzDecision{Allowed: allow}, nil
	}
	return AuthzDecision{Allowed: r.allowAll}, nil
}
func (r *fakeClusterRels) BatchCheck(_ context.Context, req AuthzBatchCheckRequest) (AuthzBatchCheckResult, error) {
	decisions := make([]AuthzDecision, len(req.Checks))
	for i, c := range req.Checks {
		d, _ := r.Check(context.Background(), AuthzCheckRequest{Permission: c.Permission})
		decisions[i] = d
	}
	return AuthzBatchCheckResult{Decisions: decisions}, nil
}
func (r *fakeClusterRels) WriteRelationships(_ context.Context, rels ...AuthzRelationship) (AuthzWriteResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.writeErr != nil {
		return AuthzWriteResult{}, r.writeErr
	}
	r.written = append(r.written, rels...)
	return AuthzWriteResult{Written: len(rels)}, nil
}
func (r *fakeClusterRels) DeleteRelationships(context.Context, AuthzRelationshipFilter) (AuthzWriteResult, error) {
	return AuthzWriteResult{}, nil
}
func (r *fakeClusterRels) RevokeResource(_ context.Context, resource AuthzObjectRef) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.revoked = append(r.revoked, resource)
	return nil
}
func (r *fakeClusterRels) LookupResources(context.Context, AuthzLookupResourcesRequest) (AuthzLookupResourcesResult, error) {
	return AuthzLookupResourcesResult{}, nil
}
func (r *fakeClusterRels) ReadRelationships(context.Context, AuthzRelationshipFilter, int, string) ([]AuthzRelationship, string, error) {
	return nil, "", nil
}

// revString is a tiny int→string helper to avoid strconv import.
func revString(n int64) string {
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

func testClusterUsecase(t *testing.T, repo *fakeClusterRepo, store *fakeCredStore, provider *fakeProvider, rels *fakeClusterRels, outbox *fakeOutbox) *ClusterUsecase {
	t.Helper()
	return NewClusterUsecase(repo, store, fakeEndpoint{}, provider, outbox, rels, logx.Noop(), ClusterUsecaseOptions{
		MaxScan: 10, MaxHydrateRounds: 3, ProbeTimeout: 5 * time.Second,
	})
}

func testPrincipal() authn.Principal {
	return authn.Principal{SubjectType: authn.SubjectTypeUser, SubjectID: "user-1", OrgID: "org-1"}
}

func TestCreateCluster_FiveSteps_ReadyOnProbeSuccess(t *testing.T) {
	repo := newFakeClusterRepo()
	store := newFakeCredStore()
	provider := &fakeProvider{probeResult: kubernetesx.ProbeResult{ClusterUID: "uid-abc", ServerVersion: kubernetesx.VersionInfo{GitVersion: "v1.30.0"}}}
	rels := newFakeClusterRels()
	outbox := &fakeOutbox{}
	uc := testClusterUsecase(t, repo, store, provider, rels, outbox)

	cred := kubernetesx.Credential{Kind: kubernetesx.CredentialKindServiceAccount, Host: "https://10.0.0.1:6443", Token: "tok"}
	c := &Cluster{ID: "c1", OrgID: "org-1", Name: "prod", ServerURL: "https://10.0.0.1:6443"}
	created, err := uc.CreateCluster(context.Background(), testPrincipal(), c, cred)
	if err != nil {
		t.Fatalf("CreateCluster error = %v", err)
	}
	if created.Status != ClusterStatusReady {
		t.Fatalf("status = %q, want READY", created.Status)
	}
	if created.ClusterUID != "uid-abc" {
		t.Fatalf("ClusterUID = %q, want uid-abc", created.ClusterUID)
	}
	if created.KubernetesVersion != "v1.30.0" {
		t.Fatalf("KubernetesVersion = %q, want v1.30.0", created.KubernetesVersion)
	}
	if store.putCalls != 1 {
		t.Fatalf("credStore.Put calls = %d, want 1", store.putCalls)
	}
	if len(store.deletes) != 0 {
		t.Fatalf("compensate deletes = %v, want none on success", store.deletes)
	}
	if len(rels.written) != 2 {
		t.Fatalf("rels written = %d, want 2 (owner+zone)", len(rels.written))
	}
}

func TestCreateCluster_ProbeFailure_MarksDegradedNoRollback(t *testing.T) {
	repo := newFakeClusterRepo()
	store := newFakeCredStore()
	provider := &fakeProvider{probeErr: errors.New("connection refused")}
	rels := newFakeClusterRels()
	outbox := &fakeOutbox{}
	uc := testClusterUsecase(t, repo, store, provider, rels, outbox)

	cred := kubernetesx.Credential{Kind: kubernetesx.CredentialKindServiceAccount, Host: "https://10.0.0.1:6443", Token: "tok"}
	c := &Cluster{ID: "c2", OrgID: "org-1", Name: "fail", ServerURL: "https://10.0.0.1:6443"}
	created, err := uc.CreateCluster(context.Background(), testPrincipal(), c, cred)
	if err != nil {
		t.Fatalf("CreateCluster probe-fail should not return error, got %v", err)
	}
	if created.Status != ClusterStatusDegraded {
		t.Fatalf("status = %q, want DEGRADED", created.Status)
	}
	// The cluster row + credential MUST stay (no rollback on probe failure).
	if _, err := repo.GetCluster(context.Background(), "c2"); err != nil {
		t.Fatalf("cluster row missing after probe failure: %v", err)
	}
	if len(store.deletes) != 0 {
		t.Fatalf("compensate deletes = %v, want none on probe failure", store.deletes)
	}
}

func TestCreateCluster_AuthzFailure_CompensatesCredential(t *testing.T) {
	repo := newFakeClusterRepo()
	store := newFakeCredStore()
	provider := &fakeProvider{probeResult: kubernetesx.ProbeResult{ClusterUID: "uid"}}
	rels := newFakeClusterRels()
	rels.writeErr = errors.New("spicedb unavailable")
	outbox := &fakeOutbox{}
	uc := testClusterUsecase(t, repo, store, provider, rels, outbox)

	cred := kubernetesx.Credential{Kind: kubernetesx.CredentialKindServiceAccount, Host: "https://10.0.0.1:6443", Token: "tok"}
	c := &Cluster{ID: "c3", OrgID: "org-1", Name: "authzfail", ServerURL: "https://10.0.0.1:6443"}
	_, err := uc.CreateCluster(context.Background(), testPrincipal(), c, cred)
	if err == nil {
		t.Fatal("CreateCluster with authz failure: want error, got nil")
	}
	// Compensate: credential deleted, cluster marked FAILED.
	if len(store.deletes) != 1 {
		t.Fatalf("compensate deletes = %d, want 1", len(store.deletes))
	}
	got, _ := repo.GetCluster(context.Background(), "c3")
	if got.Status != ClusterStatusFailed {
		t.Fatalf("compensate status = %q, want FAILED", got.Status)
	}
}

func TestRotateCredential_UIDMismatch_RejectsAndCleansUp(t *testing.T) {
	repo := newFakeClusterRepo()
	// Seed an existing cluster at revision 1 with a known UID.
	repo.clusters["c-rot"] = &Cluster{ID: "c-rot", OrgID: "org-1", Name: "rot", ServerURL: "https://10.0.0.1:6443", CredentialRef: "old-ref", CredentialRevision: 1, Revision: 1, ClusterUID: "uid-original", Status: ClusterStatusReady}
	store := newFakeCredStore()
	// Probe returns a DIFFERENT cluster_uid → mismatch.
	provider := &fakeProvider{probeResult: kubernetesx.ProbeResult{ClusterUID: "uid-different"}}
	rels := newFakeClusterRels()
	outbox := &fakeOutbox{}
	uc := testClusterUsecase(t, repo, store, provider, rels, outbox)

	newCred := kubernetesx.Credential{Kind: kubernetesx.CredentialKindServiceAccount, Host: "https://10.0.0.1:6443", Token: "tok2"}
	_, err := uc.RotateCredential(context.Background(), testPrincipal(), "c-rot", 1, newCred)
	if err == nil {
		t.Fatal("RotateCredential with UID mismatch: want error, got nil")
	}
	// The new credential must be cleaned up (compensate Delete).
	if len(store.deletes) != 1 {
		t.Fatalf("compensate deletes = %d, want 1 (new credential)", len(store.deletes))
	}
	// The cluster row must NOT be updated (no credCalls).
	if len(repo.credCalls) != 0 {
		t.Fatalf("credCalls = %d, want 0 (DB not stamped on mismatch)", len(repo.credCalls))
	}
}

func TestRotateCredential_CASConflict_RollsBackCredential(t *testing.T) {
	repo := newFakeClusterRepo()
	// Seed cluster at revision 1 (pre-check passes); force DB CAS to fail via
	// credCASFail so the compensate path runs after Put + probe succeed.
	repo.clusters["c-cas"] = &Cluster{ID: "c-cas", OrgID: "org-1", Name: "cas", ServerURL: "https://10.0.0.1:6443", CredentialRef: "old", CredentialRevision: 1, Revision: 1, ClusterUID: "uid", Status: ClusterStatusReady}
	repo.credCASFail = true
	store := newFakeCredStore()
	provider := &fakeProvider{probeResult: kubernetesx.ProbeResult{ClusterUID: "uid"}} // match, probe ok
	rels := newFakeClusterRels()
	outbox := &fakeOutbox{}
	uc := testClusterUsecase(t, repo, store, provider, rels, outbox)

	newCred := kubernetesx.Credential{Kind: kubernetesx.CredentialKindServiceAccount, Host: "https://10.0.0.1:6443", Token: "tok2"}
	_, err := uc.RotateCredential(context.Background(), testPrincipal(), "c-cas", 1, newCred)
	if err == nil {
		t.Fatal("RotateCredential with DB CAS failure: want error, got nil")
	}
	// Probe succeeded so the new credential was Put; DB CAS fail must Delete it.
	if len(store.deletes) != 1 {
		t.Fatalf("compensate deletes = %d, want 1 (new credential rolled back)", len(store.deletes))
	}
	if len(repo.credCalls) != 1 {
		t.Fatalf("credCalls = %d, want 1 (DB CAS attempted)", len(repo.credCalls))
	}
}

func TestDeleteCluster_BlocksHardDeleteWithNamespaces(t *testing.T) {
	repo := newFakeClusterRepo()
	repo.clusters["c-del"] = &Cluster{ID: "c-del", OrgID: "org-1", Name: "del", ServerURL: "https://10.0.0.1:6443", CredentialRef: "r", CredentialRevision: 1, Revision: 1, Status: ClusterStatusReady}
	repo.nsCount["c-del"] = 3
	store := newFakeCredStore()
	provider := &fakeProvider{}
	rels := newFakeClusterRels()
	outbox := &fakeOutbox{}
	uc := testClusterUsecase(t, repo, store, provider, rels, outbox)

	// No policy (hard delete) with namespaces → ErrFailedPrecondition.
	err := uc.DeleteCluster(context.Background(), testPrincipal(), "c-del", "")
	if !errors.Is(err, ErrClusterFailedPrecondition) {
		t.Fatalf("hard delete with namespaces error = %v, want ErrClusterFailedPrecondition", err)
	}
	if repo.softDeleted["c-del"] {
		t.Fatal("cluster soft-deleted despite hard-delete guard")
	}
}

func TestDeleteCluster_DetachOnlyWithNamespaces_SucceedsNoOutbox(t *testing.T) {
	repo := newFakeClusterRepo()
	repo.clusters["c-det"] = &Cluster{ID: "c-det", OrgID: "org-1", Name: "det", ServerURL: "https://10.0.0.1:6443", CredentialRef: "r", CredentialRevision: 1, Revision: 1, Status: ClusterStatusReady}
	repo.nsCount["c-det"] = 2
	store := newFakeCredStore()
	provider := &fakeProvider{}
	rels := newFakeClusterRels()
	outbox := &fakeOutbox{}
	uc := testClusterUsecase(t, repo, store, provider, rels, outbox)

	if err := uc.DeleteCluster(context.Background(), testPrincipal(), "c-det", DeletePolicyDetachOnly); err != nil {
		t.Fatalf("DeleteCluster DETACH_ONLY error = %v", err)
	}
	if !repo.softDeleted["c-det"] {
		t.Fatal("cluster not soft-deleted on DETACH_ONLY")
	}
	// DETACH_ONLY does NOT enqueue namespace cleanup.
	outbox.mu.Lock()
	defer outbox.mu.Unlock()
	if len(outbox.events) != 0 {
		t.Fatalf("DETACH_ONLY enqueued outbox = %v, want none", outbox.events)
	}
}

func TestDeleteCluster_CascadeWithNamespaces_SucceedsAndEnqueuesOutbox(t *testing.T) {
	repo := newFakeClusterRepo()
	repo.clusters["c-del2"] = &Cluster{ID: "c-del2", OrgID: "org-1", Name: "del2", ServerURL: "https://10.0.0.1:6443", CredentialRef: "r", CredentialRevision: 1, Revision: 1, Status: ClusterStatusReady}
	repo.nsCount["c-del2"] = 2
	store := newFakeCredStore()
	provider := &fakeProvider{}
	rels := newFakeClusterRels()
	outbox := &fakeOutbox{}
	uc := testClusterUsecase(t, repo, store, provider, rels, outbox)

	if err := uc.DeleteCluster(context.Background(), testPrincipal(), "c-del2", DeletePolicyCascade); err != nil {
		t.Fatalf("DeleteCluster CASCADE error = %v", err)
	}
	if !repo.softDeleted["c-del2"] {
		t.Fatal("cluster not soft-deleted on CASCADE")
	}
	if len(rels.revoked) != 1 {
		t.Fatalf("rels revoked = %d, want 1", len(rels.revoked))
	}
	if len(provider.invalidate) != 1 {
		t.Fatalf("pool invalidate = %d, want 1", len(provider.invalidate))
	}
	// Outbox event for namespace cleanup enqueued.
	outbox.mu.Lock()
	defer outbox.mu.Unlock()
	if len(outbox.events) != 1 || outbox.events[0].eventType != "namespace_cleanup" {
		t.Fatalf("outbox events = %v, want one namespace_cleanup", outbox.events)
	}
}

func TestUpdateCluster_RejectsImmutableField(t *testing.T) {
	repo := newFakeClusterRepo()
	repo.clusters["c-up"] = &Cluster{ID: "c-up", OrgID: "org-1", Name: "up", Revision: 1, Status: ClusterStatusReady}
	uc := testClusterUsecase(t, repo, newFakeCredStore(), &fakeProvider{}, newFakeClusterRels(), &fakeOutbox{})

	_, err := uc.UpdateCluster(context.Background(), testPrincipal(), "c-up", 1, map[string]any{"org_id": "other"})
	if !errors.Is(err, ErrClusterInvalidArgument) {
		t.Fatalf("UpdateCluster immutable field error = %v, want ErrClusterInvalidArgument", err)
	}
}

func TestCanonicalSubject_RejectsEmptyType(t *testing.T) {
	_, err := canonicalSubject(authn.Principal{SubjectType: "", SubjectID: "x"})
	if !errors.Is(err, ErrClusterUnsupportedPrincipalType) {
		t.Fatalf("empty SubjectType error = %v, want ErrClusterUnsupportedPrincipalType", err)
	}
	_, err = canonicalSubject(authn.Principal{SubjectType: "user", SubjectID: ""})
	if !errors.Is(err, ErrClusterUnauthenticated) {
		t.Fatalf("empty SubjectID error = %v, want ErrClusterUnauthenticated", err)
	}
	sub, err := canonicalSubject(authn.Principal{SubjectType: "user", SubjectID: "u1"})
	if err != nil || sub.Type != "user" || sub.ID != "u1" {
		t.Fatalf("canonicalSubject = %+v err=%v, want user/u1", sub, err)
	}
}
