package biz

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aisphereio/kernel/logx"
)

// fakeNamespaceRepo is a biz.NamespaceRepository stand-in. It stores namespaces
// + shares in maps and records visibility/status calls so tests assert the
// three-step visibility switch and reconciler convergence.
type fakeNamespaceRepo struct {
	mu          sync.Mutex
	namespaces  map[string]*Namespace
	shares      map[string]*NamespaceShare
	visUpdates  []nsVisCall
	statusCalls []nsStatusCall
	softDeleted map[string]bool
	visCASFail  bool // when true, UpdateNamespaceVisibility always returns ErrClusterRevisionConflict
}

type nsVisCall struct {
	id           string
	expectedRev  int64
	visibility   string
	syncStatus   string
}
type nsStatusCall struct {
	id, expected, next string
}

func newFakeNamespaceRepo() *fakeNamespaceRepo {
	return &fakeNamespaceRepo{
		namespaces:  map[string]*Namespace{},
		shares:      map[string]*NamespaceShare{},
		softDeleted: map[string]bool{},
	}
}

func (r *fakeNamespaceRepo) CreateNamespace(_ context.Context, ns *Namespace) (*Namespace, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	stored := *ns
	r.namespaces[ns.ID] = &stored
	return &stored, nil
}
func (r *fakeNamespaceRepo) GetNamespace(_ context.Context, id string) (*Namespace, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ns, ok := r.namespaces[id]
	if !ok || r.softDeleted[id] {
		return nil, ErrClusterNotFound
	}
	stored := *ns
	return &stored, nil
}
func (r *fakeNamespaceRepo) GetNamespaceByClusterKubeName(_ context.Context, clusterID, kubeName string) (*Namespace, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ns := range r.namespaces {
		if ns.ClusterID == clusterID && ns.KubeName == kubeName && !r.softDeleted[ns.ID] {
			stored := *ns
			return &stored, nil
		}
	}
	return nil, ErrClusterNotFound
}
func (r *fakeNamespaceRepo) ListNamespacesByCluster(_ context.Context, clusterID string) ([]*Namespace, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []*Namespace{}
	for _, ns := range r.namespaces {
		if ns.ClusterID == clusterID && !r.softDeleted[ns.ID] {
			stored := *ns
			out = append(out, &stored)
		}
	}
	return out, nil
}
func (r *fakeNamespaceRepo) ListNamespacesByOwner(_ context.Context, ownerType, ownerID, cursor string, maxScan int) ([]*Namespace, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []*Namespace{}
	for _, ns := range r.namespaces {
		if ns.OwnerType == ownerType && ns.OwnerID == ownerID && !r.softDeleted[ns.ID] {
			stored := *ns
			out = append(out, &stored)
		}
	}
	return out, "", nil
}
func (r *fakeNamespaceRepo) UpdateNamespaceWithCAS(_ context.Context, id string, expected int64, updates map[string]any) (*Namespace, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ns, ok := r.namespaces[id]
	if !ok || ns.Revision != expected {
		return nil, ErrClusterRevisionConflict
	}
	ns.Revision++
	return &(*ns), nil
}
func (r *fakeNamespaceRepo) UpdateNamespaceVisibility(_ context.Context, id string, expected int64, visibility, syncStatus string) (*Namespace, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ns, ok := r.namespaces[id]
	if !ok {
		return nil, ErrClusterNotFound
	}
	r.visUpdates = append(r.visUpdates, nsVisCall{id, expected, visibility, syncStatus})
	// expected == 0 means skip CAS (compensate path).
	if expected != 0 && ns.Revision != expected {
		return nil, ErrClusterRevisionConflict
	}
	if r.visCASFail {
		return nil, ErrClusterRevisionConflict
	}
	if visibility != "" {
		ns.Visibility = visibility
	}
	ns.VisibilitySyncStatus = syncStatus
	ns.Revision++
	return &(*ns), nil
}
func (r *fakeNamespaceRepo) UpdateNamespaceStatus(_ context.Context, id, expected, next string, extra map[string]any) (*Namespace, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ns, ok := r.namespaces[id]
	if !ok {
		return nil, ErrClusterNotFound
	}
	r.statusCalls = append(r.statusCalls, nsStatusCall{id, expected, next})
	if expected != "" && ns.Lifecycle != expected {
		return nil, ErrClusterNotFound
	}
	ns.Lifecycle = next
	if v, ok := extra["last_error_code"]; ok {
		ns.LastErrorCode = v.(string)
	}
	if v, ok := extra["last_error_message"]; ok {
		ns.LastErrorMessage = v.(string)
	}
	return &(*ns), nil
}
func (r *fakeNamespaceRepo) SoftDeleteNamespace(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.namespaces[id]; !ok {
		return ErrClusterNotFound
	}
	r.softDeleted[id] = true
	r.namespaces[id].Lifecycle = NamespaceLifecycleDeleted
	return nil
}
func (r *fakeNamespaceRepo) CreateShare(_ context.Context, share *NamespaceShare) (*NamespaceShare, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	stored := *share
	r.shares[share.ID] = &stored
	return &stored, nil
}
func (r *fakeNamespaceRepo) DeleteShare(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.shares, id)
	return nil
}
func (r *fakeNamespaceRepo) ListSharesByNamespace(_ context.Context, namespaceID string) ([]*NamespaceShare, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []*NamespaceShare{}
	for _, s := range r.shares {
		if s.NamespaceID == namespaceID {
			stored := *s
			out = append(out, &stored)
		}
	}
	return out, nil
}
func (r *fakeNamespaceRepo) ListNamespacesBySyncStatus(_ context.Context, syncStatus string, limit int) ([]*Namespace, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []*Namespace{}
	for _, ns := range r.namespaces {
		if ns.VisibilitySyncStatus == syncStatus && !r.softDeleted[ns.ID] {
			stored := *ns
			out = append(out, &stored)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}
func (r *fakeNamespaceRepo) ListSharesBySyncStatus(_ context.Context, syncStatus string, limit int) ([]*NamespaceShare, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []*NamespaceShare{}
	for _, s := range r.shares {
		if s.SyncStatus == syncStatus {
			stored := *s
			out = append(out, &stored)
		}
	}
	return out, nil
}

// fakeNamespaceRels extends fakeClusterRels with the NamespaceRelationships
// surface (DeleteRelationships recording + LookupSubjects). writeFailOnRelation
// fails only wildcard projection writes so the visibility compensate path is
// testable without breaking the owner-write in CreateNamespace.
type fakeNamespaceRels struct {
	*fakeClusterRels
	deletedFilters     []AuthzRelationshipFilter
	writeFailOnRelation string
	writeFailErr       error
	deleteFailErr      error
}

func newFakeNamespaceRels() *fakeNamespaceRels {
	return &fakeNamespaceRels{fakeClusterRels: newFakeClusterRels()}
}

func (r *fakeNamespaceRels) WriteRelationships(ctx context.Context, rels ...AuthzRelationship) (AuthzWriteResult, error) {
	r.mu.Lock()
	failRel := r.writeFailOnRelation
	failErr := r.writeFailErr
	r.mu.Unlock()
	if failRel != "" {
		for _, rel := range rels {
			if rel.Relation == failRel {
				return AuthzWriteResult{}, failErr
			}
		}
	}
	return r.fakeClusterRels.WriteRelationships(ctx, rels...)
}

func (r *fakeNamespaceRels) DeleteRelationships(_ context.Context, filter AuthzRelationshipFilter) (AuthzWriteResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deletedFilters = append(r.deletedFilters, filter)
	if r.deleteFailErr != nil {
		return AuthzWriteResult{}, r.deleteFailErr
	}
	return AuthzWriteResult{}, nil
}

func (r *fakeNamespaceRels) LookupSubjects(context.Context, AuthzLookupSubjectsRequest) (AuthzLookupSubjectsResult, error) {
	return AuthzLookupSubjectsResult{}, nil
}

func testNamespaceUsecase(t *testing.T, nsRepo *fakeNamespaceRepo, clRepo *fakeClusterRepo, provider *fakeProvider, rels *fakeNamespaceRels, outbox *fakeOutbox) *NamespaceUsecase {
	t.Helper()
	return NewNamespaceUsecase(nsRepo, clRepo, provider, outbox, rels, logx.Noop(), ClusterUsecaseOptions{
		MaxScan: 10, MaxHydrateRounds: 3, ProbeTimeout: 5 * time.Second,
	})
}

// seedNamespace inserts a namespace row directly into the fake repo.
func seedNamespace(r *fakeNamespaceRepo, ns *Namespace) {
	r.mu.Lock()
	defer r.mu.Unlock()
	stored := *ns
	r.namespaces[ns.ID] = &stored
}

func TestCreateNamespace_ReadyOnSuccess(t *testing.T) {
	clRepo := newFakeClusterRepo()
	clRepo.clusters["cl-1"] = &Cluster{ID: "cl-1", OrgID: "org-1", Name: "prod", Status: ClusterStatusReady}
	nsRepo := newFakeNamespaceRepo()
	provider := &fakeProvider{}
	rels := newFakeNamespaceRels()
	outbox := &fakeOutbox{}
	uc := testNamespaceUsecase(t, nsRepo, clRepo, provider, rels, outbox)

	ns := &Namespace{ID: "ns-1", ClusterID: "cl-1", KubeName: "team-alpha", DisplayName: "Team Alpha"}
	out, err := uc.CreateNamespace(context.Background(), testPrincipal(), ns)
	if err != nil {
		t.Fatalf("CreateNamespace error = %v", err)
	}
	if out.Lifecycle != NamespaceLifecycleReady {
		t.Fatalf("lifecycle = %s, want READY", out.Lifecycle)
	}
	if out.Visibility != NamespaceVisibilityPrivate {
		t.Fatalf("visibility = %s, want PRIVATE (default)", out.Visibility)
	}
	if out.VisibilitySyncStatus != VisibilitySyncSynced {
		t.Fatalf("sync_status = %s, want SYNCED", out.VisibilitySyncStatus)
	}
	// Owner + parent relationships written (2 rels).
	rels.mu.Lock()
	defer rels.mu.Unlock()
	if len(rels.written) != 2 {
		t.Fatalf("rels written = %d, want 2 (owner + parent)", len(rels.written))
	}
}

func TestCreateNamespace_AuthzFailure_MarksFailed(t *testing.T) {
	clRepo := newFakeClusterRepo()
	clRepo.clusters["cl-1"] = &Cluster{ID: "cl-1", OrgID: "org-1", Name: "prod", Status: ClusterStatusReady}
	nsRepo := newFakeNamespaceRepo()
	provider := &fakeProvider{}
	rels := newFakeNamespaceRels()
	rels.writeFailOnRelation = "owner" // fail the Step 4 owner+parent write
	rels.writeFailErr = errors.New("spicedb unavailable")
	outbox := &fakeOutbox{}
	uc := testNamespaceUsecase(t, nsRepo, clRepo, provider, rels, outbox)

	ns := &Namespace{ID: "ns-2", ClusterID: "cl-1", KubeName: "team-beta"}
	out, err := uc.CreateNamespace(context.Background(), testPrincipal(), ns)
	if err == nil {
		t.Fatal("CreateNamespace expected error on authz failure")
	}
	if out != nil {
		t.Fatal("CreateNamespace returned non-nil namespace on authz failure (expected nil)")
	}
	// Row still exists but stamped FAILED + error code recorded (compensate).
	stored, _ := nsRepo.GetNamespace(context.Background(), "ns-2")
	if stored.Lifecycle != NamespaceLifecycleFailed {
		t.Fatalf("lifecycle = %s, want FAILED", stored.Lifecycle)
	}
	if stored.LastErrorCode != "AUTHZ_PROJECTION_FAILED" {
		t.Fatalf("last_error_code = %s, want AUTHZ_PROJECTION_FAILED", stored.LastErrorCode)
	}
	// RevokeResource called as compensate.
	rels.mu.Lock()
	defer rels.mu.Unlock()
	if len(rels.revoked) != 1 || rels.revoked[0].ID != "ns-2" {
		t.Fatalf("revoked = %v, want [ns-2]", rels.revoked)
	}
}

func TestCreateNamespace_RejectsBadKubeName(t *testing.T) {
	clRepo := newFakeClusterRepo()
	nsRepo := newFakeNamespaceRepo()
	rels := newFakeNamespaceRels()
	uc := testNamespaceUsecase(t, nsRepo, clRepo, &fakeProvider{}, rels, &fakeOutbox{})

	badNames := []string{"Team_Alpha", "team alpha", "-leading", "trailing-", "UPPER", "a" + stringsRepeat("b", 64)}
	for _, name := range badNames {
		ns := &Namespace{ID: "ns-x", ClusterID: "cl-1", KubeName: name}
		if _, err := uc.CreateNamespace(context.Background(), testPrincipal(), ns); err == nil {
			t.Fatalf("CreateNamespace with kube_name=%q expected DNS-1123 error", name)
		}
	}
}

func TestUpdateNamespaceVisibility_PrivateToPublic_StepsAndSynced(t *testing.T) {
	nsRepo := newFakeNamespaceRepo()
	seedNamespace(nsRepo, &Namespace{ID: "ns-3", ClusterID: "cl-1", KubeName: "team-gamma", Visibility: NamespaceVisibilityPrivate, VisibilitySyncStatus: VisibilitySyncSynced, Lifecycle: NamespaceLifecycleReady, Revision: 1})
	clRepo := newFakeClusterRepo()
	provider := &fakeProvider{}
	rels := newFakeNamespaceRels()
	outbox := &fakeOutbox{}
	uc := testNamespaceUsecase(t, nsRepo, clRepo, provider, rels, outbox)

	out, err := uc.UpdateNamespaceVisibility(context.Background(), testPrincipal(), "ns-3", 1, NamespaceVisibilityPublic)
	if err != nil {
		t.Fatalf("UpdateNamespaceVisibility PRIVATE→PUBLIC error = %v", err)
	}
	if out.Visibility != NamespaceVisibilityPublic {
		t.Fatalf("visibility = %s, want PUBLIC", out.Visibility)
	}
	if out.VisibilitySyncStatus != VisibilitySyncSynced {
		t.Fatalf("sync_status = %s, want SYNCED", out.VisibilitySyncStatus)
	}
	// Two DB visibility stamps: PUBLISHING then SYNCED.
	nsRepo.mu.Lock()
	defer nsRepo.mu.Unlock()
	if len(nsRepo.visUpdates) != 2 {
		t.Fatalf("visUpdates = %d, want 2 (PUBLISHING + SYNCED)", len(nsRepo.visUpdates))
	}
	if nsRepo.visUpdates[0].syncStatus != VisibilitySyncPublishing {
		t.Fatalf("first stamp = %s, want PUBLISHING", nsRepo.visUpdates[0].syncStatus)
	}
	if nsRepo.visUpdates[1].syncStatus != VisibilitySyncSynced {
		t.Fatalf("second stamp = %s, want SYNCED", nsRepo.visUpdates[1].syncStatus)
	}
	// Wildcard viewer relationship written.
	rels.mu.Lock()
	defer rels.mu.Unlock()
	var wildcardWritten bool
	for _, rel := range rels.written {
		if rel.Relation == "viewer" && rel.Subject.Relation == "..." {
			wildcardWritten = true
		}
	}
	if !wildcardWritten {
		t.Fatal("wildcard viewer relationship not written")
	}
	// Outbox enqueued for visibility_sync.
	outbox.mu.Lock()
	defer outbox.mu.Unlock()
	if len(outbox.events) != 1 || outbox.events[0].eventType != "visibility_sync" {
		t.Fatalf("outbox = %v, want one visibility_sync", outbox.events)
	}
}

func TestUpdateNamespaceVisibility_PublicToPrivate_StepsAndSynced(t *testing.T) {
	nsRepo := newFakeNamespaceRepo()
	seedNamespace(nsRepo, &Namespace{ID: "ns-4", ClusterID: "cl-1", KubeName: "team-delta", Visibility: NamespaceVisibilityPublic, VisibilitySyncStatus: VisibilitySyncSynced, Lifecycle: NamespaceLifecycleReady, Revision: 1})
	clRepo := newFakeClusterRepo()
	provider := &fakeProvider{}
	rels := newFakeNamespaceRels()
	outbox := &fakeOutbox{}
	uc := testNamespaceUsecase(t, nsRepo, clRepo, provider, rels, outbox)

	out, err := uc.UpdateNamespaceVisibility(context.Background(), testPrincipal(), "ns-4", 1, NamespaceVisibilityPrivate)
	if err != nil {
		t.Fatalf("UpdateNamespaceVisibility PUBLIC→PRIVATE error = %v", err)
	}
	if out.Visibility != NamespaceVisibilityPrivate {
		t.Fatalf("visibility = %s, want PRIVATE", out.Visibility)
	}
	if out.VisibilitySyncStatus != VisibilitySyncSynced {
		t.Fatalf("sync_status = %s, want SYNCED", out.VisibilitySyncStatus)
	}
	// Two DB stamps: REVOKING then SYNCED.
	nsRepo.mu.Lock()
	defer nsRepo.mu.Unlock()
	if len(nsRepo.visUpdates) != 2 {
		t.Fatalf("visUpdates = %d, want 2", len(nsRepo.visUpdates))
	}
	if nsRepo.visUpdates[0].syncStatus != VisibilitySyncRevoking {
		t.Fatalf("first stamp = %s, want REVOKING", nsRepo.visUpdates[0].syncStatus)
	}
	// Wildcard viewer relationship deleted.
	rels.mu.Lock()
	defer rels.mu.Unlock()
	if len(rels.deletedFilters) != 1 {
		t.Fatalf("deletedFilters = %d, want 1", len(rels.deletedFilters))
	}
	f := rels.deletedFilters[0]
	if f.Relation != "viewer" || f.SubjectRelation != "..." {
		t.Fatalf("delete filter = %+v, want viewer/... wildcard", f)
	}
}

func TestUpdateNamespaceVisibility_SpiceDBFail_MarksSyncFailed(t *testing.T) {
	nsRepo := newFakeNamespaceRepo()
	seedNamespace(nsRepo, &Namespace{ID: "ns-5", ClusterID: "cl-1", KubeName: "team-eps", Visibility: NamespaceVisibilityPrivate, VisibilitySyncStatus: VisibilitySyncSynced, Lifecycle: NamespaceLifecycleReady, Revision: 1})
	clRepo := newFakeClusterRepo()
	provider := &fakeProvider{}
	rels := newFakeNamespaceRels()
	rels.writeFailOnRelation = "viewer" // fail the wildcard projection
	rels.writeFailErr = errors.New("spicedb timeout")
	outbox := &fakeOutbox{}
	uc := testNamespaceUsecase(t, nsRepo, clRepo, provider, rels, outbox)

	out, err := uc.UpdateNamespaceVisibility(context.Background(), testPrincipal(), "ns-5", 1, NamespaceVisibilityPublic)
	// Compensate path returns the namespace (not an error) with SYNC_FAILED.
	if err != nil {
		t.Fatalf("UpdateNamespaceVisibility SpiceDB-fail error = %v (compensate returns nil err)", err)
	}
	if out == nil {
		t.Fatal("returned nil namespace")
	}
	if out.VisibilitySyncStatus != VisibilitySyncFailed {
		t.Fatalf("sync_status = %s, want SYNC_FAILED", out.VisibilitySyncStatus)
	}
	// Compensate reverses visibility to prior (PRIVATE).
	if out.Visibility != NamespaceVisibilityPrivate {
		t.Fatalf("visibility = %s, want PRIVATE (reverted by compensate)", out.Visibility)
	}
	// No outbox enqueued on failure.
	outbox.mu.Lock()
	defer outbox.mu.Unlock()
	if len(outbox.events) != 0 {
		t.Fatalf("outbox = %v, want none on SpiceDB failure", outbox.events)
	}
}

func TestDeleteNamespace_Cascade_RemoteDeleteAndRevoke(t *testing.T) {
	nsRepo := newFakeNamespaceRepo()
	seedNamespace(nsRepo, &Namespace{ID: "ns-6", ClusterID: "cl-1", KubeName: "team-zeta", Visibility: NamespaceVisibilityPrivate, Lifecycle: NamespaceLifecycleReady, Managed: true, Revision: 1})
	clRepo := newFakeClusterRepo()
	provider := &fakeProvider{}
	rels := newFakeNamespaceRels()
	uc := testNamespaceUsecase(t, nsRepo, clRepo, provider, rels, &fakeOutbox{})

	if err := uc.DeleteNamespace(context.Background(), testPrincipal(), "ns-6", DeletePolicyCascade); err != nil {
		t.Fatalf("DeleteNamespace CASCADE error = %v", err)
	}
	if !nsRepo.softDeleted["ns-6"] {
		t.Fatal("namespace not soft-deleted")
	}
	// RevokeResource called on the namespace.
	rels.mu.Lock()
	defer rels.mu.Unlock()
	if len(rels.revoked) != 1 || rels.revoked[0].ID != "ns-6" {
		t.Fatalf("revoked = %v, want [ns-6]", rels.revoked)
	}
}

func TestReconciler_ConvergesPublishing_ProjectsPublicWildcard(t *testing.T) {
	nsRepo := newFakeNamespaceRepo()
	seedNamespace(nsRepo, &Namespace{ID: "ns-r1", ClusterID: "cl-1", KubeName: "team-rec1", Visibility: NamespaceVisibilityPublic, VisibilitySyncStatus: VisibilitySyncPublishing, Lifecycle: NamespaceLifecycleReady, Revision: 1})
	rels := newFakeNamespaceRels()
	rec := NewVisibilityReconciler(nsRepo, rels, nil, logx.Noop(), 10)

	if err := rec.Run(context.Background()); err != nil {
		t.Fatalf("reconciler Run error = %v", err)
	}
	// Wildcard viewer written by reconciler.
	rels.mu.Lock()
	defer rels.mu.Unlock()
	var wildcardWritten bool
	for _, rel := range rels.written {
		if rel.Relation == "viewer" && rel.Subject.Relation == "..." && rel.Resource.ID == "ns-r1" {
			wildcardWritten = true
		}
	}
	if !wildcardWritten {
		t.Fatal("reconciler did not project PUBLIC wildcard")
	}
	// Stamped SYNCED.
	stored, _ := nsRepo.GetNamespace(context.Background(), "ns-r1")
	if stored.VisibilitySyncStatus != VisibilitySyncSynced {
		t.Fatalf("sync_status = %s, want SYNCED after reconcile", stored.VisibilitySyncStatus)
	}
}

func TestReconciler_ConvergesRevoking_DeletesPublicWildcard(t *testing.T) {
	nsRepo := newFakeNamespaceRepo()
	seedNamespace(nsRepo, &Namespace{ID: "ns-r2", ClusterID: "cl-1", KubeName: "team-rec2", Visibility: NamespaceVisibilityPrivate, VisibilitySyncStatus: VisibilitySyncRevoking, Lifecycle: NamespaceLifecycleReady, Revision: 1})
	rels := newFakeNamespaceRels()
	rec := NewVisibilityReconciler(nsRepo, rels, nil, logx.Noop(), 10)

	if err := rec.Run(context.Background()); err != nil {
		t.Fatalf("reconciler Run error = %v", err)
	}
	// Wildcard viewer deleted by reconciler.
	rels.mu.Lock()
	defer rels.mu.Unlock()
	if len(rels.deletedFilters) != 1 {
		t.Fatalf("deletedFilters = %d, want 1", len(rels.deletedFilters))
	}
	f := rels.deletedFilters[0]
	if f.ResourceID != "ns-r2" || f.Relation != "viewer" {
		t.Fatalf("delete filter = %+v, want ns-r2/viewer", f)
	}
	// Stamped SYNCED.
	stored, _ := nsRepo.GetNamespace(context.Background(), "ns-r2")
	if stored.VisibilitySyncStatus != VisibilitySyncSynced {
		t.Fatalf("sync_status = %s, want SYNCED after reconcile", stored.VisibilitySyncStatus)
	}
}

func TestReconciler_ConvergesSyncFailed_RetriesAndSyncs(t *testing.T) {
	nsRepo := newFakeNamespaceRepo()
	seedNamespace(nsRepo, &Namespace{ID: "ns-r3", ClusterID: "cl-1", KubeName: "team-rec3", Visibility: NamespaceVisibilityPublic, VisibilitySyncStatus: VisibilitySyncFailed, Lifecycle: NamespaceLifecycleReady, Revision: 1})
	rels := newFakeNamespaceRels()
	rec := NewVisibilityReconciler(nsRepo, rels, nil, logx.Noop(), 10)

	if err := rec.Run(context.Background()); err != nil {
		t.Fatalf("reconciler Run error = %v", err)
	}
	stored, _ := nsRepo.GetNamespace(context.Background(), "ns-r3")
	if stored.VisibilitySyncStatus != VisibilitySyncSynced {
		t.Fatalf("sync_status = %s, want SYNCED after reconcile from SYNC_FAILED", stored.VisibilitySyncStatus)
	}
}

// stringsRepeat is a tiny strings.Repeat stand-in to avoid importing strings.
func stringsRepeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}
