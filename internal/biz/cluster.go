package biz

import (
	"context"
	"time"

	"github.com/aisphereio/kernel/errorx"
	"github.com/aisphereio/kernel/kubernetesx"
)

// Cluster lifecycle + credential store contracts (design §5.5/§5.7/§12.4).
// The biz layer depends only on these narrow interfaces; the data layer
// provides the AEAD store, SSRF-aware endpoint policy, and kubernetesx.Client
// pool implementations. This file freezes the interface contract so PR 3/3
// (Cluster CRUD) can build against it without touching PR 2/3 internals.

// CredentialLocator identifies a stored credential revision. CredentialRef is
// allocated by ClusterCredentialStore (via NewCredentialRef / Put); the biz
// layer never invents refs itself. AAD is reconstructed by the Store from
// {ClusterID, Ref, CredentialRevision} — callers never supply AAD
// (design §5.5).
type CredentialLocator struct {
	ClusterID          string
	CredentialRef      string
	CredentialRevision int64
}

// ClusterCredentialStore persists cluster credentials with versioned AEAD
// (design §5.5 V1: AES-256-GCM, no KMS/Vault). Plaintext never lands in DB or
// logs; AAD binds cluster_id + ref + credential_revision so ciphertext cannot
// be replayed against a different credential revision or cluster.
type ClusterCredentialStore interface {
	// NewCredentialRef allocates a fresh UUIDv4 ref without persisting
	// anything. Used by CreateCluster so the k8s_clusters row (whose
	// credential_ref is NOT NULL) can be INSERTed first inside a tx; the
	// matching credential row is then written via PutWithRef in the same tx,
	// satisfying the k8s_cluster_credentials.cluster_id FK (Postgres checks
	// FKs immediately, even inside a tx, so the cluster row must exist before
	// the credential row is inserted — design §5.7.2).
	NewCredentialRef() (string, error)

	// Put encrypts value under a freshly allocated ref, using
	// {clusterID, newRef, credentialRevision} as AAD. Returns the full
	// Locator so the biz layer can persist credential_ref + credential_revision
	// in the k8s_clusters row. credentialRevision is the *target* revision
	// (Put does not increment it; the caller chooses 1 for create, current+1
	// for rotate — design §5.5).
	Put(ctx context.Context, clusterID string, credentialRevision int64, value kubernetesx.Credential) (CredentialLocator, error)

	// PutWithRef is Put with a caller-allocated ref. Used by CreateCluster to
	// satisfy the k8s_cluster_credentials.cluster_id FK: the caller allocates
	// the ref, INSERTs the k8s_clusters row (whose credential_ref is NOT NULL)
	// first, then calls PutWithRef inside the same tx so the credential row's
	// FK is satisfied. AAD = {clusterID, ref, credentialRevision}.
	PutWithRef(ctx context.Context, clusterID, ref string, credentialRevision int64, value kubernetesx.Credential) (CredentialLocator, error)

	// Get reconstructs AAD from locator, reads the DB row, verifies the row's
	// cluster_id/ref/credential_revision match the locator (drift →
	// ErrClusterFailedPrecondition), then decrypts. AAD is never persisted;
	// the Store rebuilds it from the Locator on every Get (design §5.5).
	Get(ctx context.Context, locator CredentialLocator) (kubernetesx.Credential, error)

	// Delete removes a credential row by ref. Used by rotate cleanup (delayed
	// via outbox, design §5.7.3 step 6) and create-compensate (design §5.7.2).
	Delete(ctx context.Context, ref string) error

	// RotateKey re-encrypts every row whose key_version == fromVersion to
	// toVersion (decrypt with old master key → re-encrypt with new master key
	// + fresh nonce → write back ciphertext/nonce/key_version). Returns the
	// count of re-encrypted rows. V1 cost is full decrypt+reencrypt per row
	// (no DEK rewrap fast path); acceptable because credential count is small
	// (design §5.5).
	RotateKey(ctx context.Context, fromVersion, toVersion string) (reencrypted int, err error)
}

// ResolvedEndpoint carries the validated IPs returned by EndpointPolicy for a
// server_url. The ClientPool uses these to pin the DialContext (DNS rebinding
// defense, design §12.4): the connection dials the resolved IP directly while
// HTTP Host header + TLS SNI keep using the original hostname.
type ResolvedEndpoint struct {
	OriginalHost string   // hostname from server_url, used for Host header + TLS SNI
	ResolvedIPs  []string // validated IPs (loopback/private/link-local already filtered)
}

// EndpointPolicy is the Hub SSRF guard (design §12.4). Validate runs before
// any Cluster is created or its client is built: it forces https, resolves
// the hostname, rejects loopback/link-local/private (unless configured
// otherwise), rejects forbidden CIDRs, and enforces the egress allowlist.
// The returned ResolvedEndpoint is cached by the ClientPool so subsequent
// client builds do not re-resolve DNS (rebinding defense).
type EndpointPolicy interface {
	Validate(ctx context.Context, serverURL string) (ResolvedEndpoint, error)
}

// NamespaceApplySpec is the biz-layer view of a Namespace to create or import
// on a remote cluster. The data layer translates this into a corev1.Namespace
// and runs SSA via kubernetesx.Client.Apply. Keeping the biz interface free of
// k8s.io/api-go types lets the biz layer stay thin and testable.
type NamespaceApplySpec struct {
	Name        string            // Kubernetes Namespace name (DNS-1123 label)
	Labels      map[string]string // AISphere-managed labels (aisphere.io/* injected by data layer)
	Annotations map[string]string
}

// NamespaceSyncResult is returned by SyncNamespaces for each remote Namespace
// discovered during a cluster sync.
type NamespaceSyncResult struct {
	Name            string
	UID             string
	ResourceVersion string
	Labels          map[string]string
}

// KubernetesProvider is the biz-facing view of the kubernetesx.Client pool
// (design §5.6). The biz layer never touches kubeconfig, kubernetesx.New, or
// connection lifecycle; it asks the pool for a probe/apply/delete and trusts
// the pool to cache + invalidate clients per cluster (revision-aware).
type KubernetesProvider interface {
	// Probe runs a reachability + auth probe against the cluster's current
	// credential (the pool reads the active credential_ref/revision from the
	// locator). For RotateCredential, biz builds a one-shot probe outside the
	// pool (design §5.7.3 step 3); that path does NOT go through this method.
	Probe(ctx context.Context, clusterID string, locator CredentialLocator, cred kubernetesx.Credential) (kubernetesx.ProbeResult, error)

	// ApplyNamespace SSA-applies a Namespace on the cluster (design §6.4
	// step 6). The data layer injects aisphere.io/* managed labels here.
	ApplyNamespace(ctx context.Context, clusterID string, locator CredentialLocator, ns NamespaceApplySpec) error

	// DeleteNamespace removes a remote Namespace by kube_name (design §6.6,
	// only for managed=true + explicit DeletePolicy).
	DeleteNamespace(ctx context.Context, clusterID string, locator CredentialLocator, kubeName string) error

	// ListNamespaces enumerates remote Namespaces for SyncNamespaces.
	ListNamespaces(ctx context.Context, clusterID string, locator CredentialLocator) ([]NamespaceSyncResult, error)

	// InvalidateCluster drops the cached client for clusterID after a
	// credential rotate (design §5.7.3 step 5) or cluster delete. The next
	// Probe/Apply/Delete rebuilds the client from the new credential.
	InvalidateCluster(ctx context.Context, clusterID string)
}

// Hub-level sentinel errors for the Kubernetes control plane (design §5.3.3).
// These are independent of Kernel KUBERNETES_* codes: Kernel codes are used
// only when Hub calls kubernetesx and normalizes a passthrough error; Hub's
// own CAS/FieldMask/principal/lifecycle judgments use the codes below.
var (
	// ErrClusterRevisionConflict: CAS failed (expected_revision mismatch, 409).
	ErrClusterRevisionConflict = errorx.Conflict(errorx.Code("REVISION_CONFLICT"), "cluster revision conflict: expected_revision does not match current revision")

	// ErrClusterInvalidArgument: parameter error (FieldMask missing, immutable
	// field in mask, invalid name, etc., 400).
	ErrClusterInvalidArgument = errorx.BadRequest(errorx.Code("INVALID_ARGUMENT"), "invalid argument")

	// ErrClusterUnsupportedPrincipalType: principal SubjectType empty or
	// unknown (design §7.2.1). 400 — the caller supplied a bad identity; not
	// 401/403 because the principal *is* authenticated, just not mappable.
	ErrClusterUnsupportedPrincipalType = errorx.BadRequest(errorx.Code("UNSUPPORTED_PRINCIPAL_TYPE"), "unsupported principal type: cannot map to SpiceDB subject")

	// ErrClusterFailedPrecondition: state does not allow the operation (has
	// Namespaces blocking hard Cluster delete, cluster_uid drift, AAD
	// mismatch on credential Get). 412 — Kernel errorx has no
	// PreconditionFailed constructor, so use NewStatus with the explicit HTTP
	// status (design §5.3.3).
	ErrClusterFailedPrecondition = errorx.NewStatus(errorx.Code("FAILED_PRECONDITION"), 412, "failed precondition: cluster state does not allow operation")

	// ErrClusterUnauthenticated: anonymous principal called a Cluster/Namespace
	// RPC (design §5.3.3). Reuses errorx.Unauthorized (401).
	ErrClusterUnauthenticated = errorx.Unauthorized(errorx.Code("UNAUTHENTICATED"), "unauthenticated: anonymous principal cannot access kubernetes API")

	// ErrClusterNotFound: cluster id does not exist (or is soft-deleted), 404.
	ErrClusterNotFound = errorx.NotFound(errorx.Code("CLUSTER_NOT_FOUND"), "cluster not found")

	// ErrNamespaceNotFound: namespace id does not exist (or is soft-deleted), 404.
	ErrNamespaceNotFound = errorx.NotFound(errorx.Code("NAMESPACE_NOT_FOUND"), "namespace not found")

	// Agent Sandbox NotFound sentinels (design §11). Each maps to a 404 for its
	// control-plane record type; returned by SandboxRepository Get/Delete/Update
	// methods when the row is missing or already soft-deleted.
	ErrSandboxTemplateNotFound = errorx.NotFound(errorx.Code("SANDBOX_TEMPLATE_NOT_FOUND"), "sandbox template not found")
	ErrSandboxNotFound         = errorx.NotFound(errorx.Code("SANDBOX_NOT_FOUND"), "sandbox not found")
	ErrWarmPoolNotFound        = errorx.NotFound(errorx.Code("WARM_POOL_NOT_FOUND"), "warm pool not found")
	ErrSandboxClaimNotFound    = errorx.NotFound(errorx.Code("SANDBOX_CLAIM_NOT_FOUND"), "sandbox claim not found")

	// ErrClusterCredentialInvalid: kernel passthrough — the credential failed
	// validation or probe (design §5.7.3 step 3). 400.
	ErrClusterCredentialInvalid = errorx.BadRequest(errorx.Code("KUBERNETES_CREDENTIAL_INVALID"), "credential invalid: probe failed or cluster_uid mismatch")
)

// ClusterUsecaseOptions carries optional dependencies for ClusterUsecase
// (constructed in PR 3/3). Kept here so the interface freeze in PR 2/3 is
// self-contained.
type ClusterUsecaseOptions struct {
	MaxScan          int
	MaxHydrateRounds int
	ProbeTimeout     time.Duration
}

// --- Domain types (PR③) ---
//
// These are the biz-layer views of k8s_clusters / k8s_namespaces /
// k8s_namespace_shares rows (design §8). The data layer (cluster_repo.go,
// namespace_repo.go) translates between these and GORM models; the service
// layer translates between these and proto messages. Status / visibility /
// lifecycle fields are uppercase strings matching the DB CHECK constraints
// and proto enums (design decision 1).

// ClusterStatus mirrors the k8s_clusters.status CHECK constraint and the
// proto ClusterStatus enum (design §8.1).
const (
	ClusterStatusCreating  = "CREATING"
	ClusterStatusReady     = "READY"
	ClusterStatusProbing   = "PROBING"
	ClusterStatusDegraded  = "DEGRADED"
	ClusterStatusDeleting  = "DELETING"
	ClusterStatusDeleted   = "DELETED"
	ClusterStatusFailed    = "FAILED"
)

// NamespaceVisibility mirrors k8s_namespaces.visibility (design §8.3).
const (
	NamespaceVisibilityPrivate = "PRIVATE"
	NamespaceVisibilityPublic  = "PUBLIC"
)

// VisibilitySyncStatus mirrors k8s_namespaces.visibility_sync_status.
const (
	VisibilitySyncSynced      = "SYNCED"
	VisibilitySyncPublishing  = "PUBLISHING"
	VisibilitySyncRevoking    = "REVOKING"
	VisibilitySyncFailed      = "SYNC_FAILED"
)

// NamespaceLifecycle mirrors k8s_namespaces.lifecycle.
const (
	NamespaceLifecycleCreating    = "CREATING"
	NamespaceLifecycleReady       = "READY"
	NamespaceLifecycleTerminating = "TERMINATING"
	NamespaceLifecycleFailed      = "FAILED"
	NamespaceLifecycleDeleted     = "DELETED"
)

// ShareRelation mirrors k8s_namespace_shares.relation CHECK constraint.
const (
	ShareRelationViewer = "viewer"
	ShareRelationUser   = "user"
	ShareRelationEditor = "editor"
)

// DeletePolicy mirrors the proto DeletePolicy enum (design §5.7.5 / §6.6).
const (
	DeletePolicyDetachOnly = "DETACH_ONLY"
	DeletePolicyCascade    = "CASCADE"
)

// Cluster is the biz-layer view of a k8s_clusters row. CredentialRef +
// CredentialRevision identify the stored credential (via CredentialLocator);
// ServerURL is the user-supplied API server URL (already validated by
// EndpointPolicy at create time). ClusterUID is the probe-discovered UID
// used to detect identity drift across rotate (design §5.7.3 step 3).
type Cluster struct {
	ID                string
	OrgID             string
	Name              string
	DisplayName       string
	Description       string
	ServerURL         string
	CredentialRef     string
	CredentialRevision int64
	Distribution      string
	KubernetesVersion string
	ClusterUID        string
	Status            string
	HealthMessage     string
	Labels            map[string]string
	LastProbeAt       time.Time
	OwnerType         string
	OwnerID           string
	CreatedByType     string
	CreatedBy         string
	CreatedAt         time.Time
	UpdatedAt         time.Time
	Revision          int64
}

// Namespace is the biz-layer view of a k8s_namespaces row.
type Namespace struct {
	ID                 string
	ClusterID          string
	KubeName           string
	DisplayName        string
	Description        string
	Visibility         string
	VisibilitySyncStatus string
	Lifecycle          string
	Managed            bool
	KubernetesUID      string
	ResourceVersion    string
	Labels             map[string]string
	Annotations        map[string]string
	OwnerType          string
	OwnerID            string
	CreatedByType      string
	CreatedBy          string
	LastSyncAt         time.Time
	LastErrorCode      string
	LastErrorMessage   string
	CreatedAt          time.Time
	UpdatedAt          time.Time
	Revision           int64
}

// NamespaceShare is the biz-layer view of a k8s_namespace_shares row.
type NamespaceShare struct {
	ID              string
	NamespaceID     string
	Relation        string
	SubjectType     string
	SubjectID       string
	SubjectRelation string
	SyncStatus      string
	CreatedByType   string
	CreatedBy       string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// ---- Agent Sandbox domain types (design §11) ----
//
// The following types and the SandboxProvider interface extend the Kubernetes
// control plane to manage Agent Sandbox CRDs (agents.x-k8s.io). Third-party
// CRD types never leak into these interfaces — the data layer translates them
// to/from unstructured.Unstructured internally.

// SandboxTemplateApplySpec is the biz-layer view of a SandboxTemplate CRD to
// SSA-apply on a remote cluster. The data layer builds an
// extensions.agents.x-k8s.io/v1beta1 SandboxTemplate from this.
type SandboxTemplateApplySpec struct {
	Name             string            // K8s SandboxTemplate name (DNS-1123 label)
	Namespace        string            // K8s namespace (usually agent-sandbox-system or target ns)
	Image            string            // Container image
	ContainerCommand []string          // Entrypoint command (e.g. ["sh","-c","sleep infinity"])
	Labels           map[string]string // AISphere-managed labels
}

// SandboxTemplateSyncResult is returned by ListSandboxTemplates for each
// remote SandboxTemplate CRD discovered during a sync.
type SandboxTemplateSyncResult struct {
	Name            string
	Namespace       string
	UID             string
	ResourceVersion string
	Image           string
	Labels          map[string]string
}

// SandboxApplySpec is the biz-layer view of a Sandbox CRD to SSA-apply. The
// data layer builds an agents.x-k8s.io/v1beta1 Sandbox from this.
type SandboxApplySpec struct {
	Name          string            // K8s Sandbox name (DNS-1123 label)
	Namespace     string            // K8s namespace to place the Sandbox in
	TemplateRef   string            // SandboxTemplate K8s name (required for non-warm-pool creation)
	OperatingMode string            // "Running" or "Suspended" (default Running)
	Labels        map[string]string // AISphere-managed labels
}

// SandboxSyncResult is returned by ListSandboxes for each remote Sandbox CRD.
type SandboxSyncResult struct {
	Name            string
	Namespace       string
	UID             string
	ResourceVersion string
	Phase           string // Ready condition reason: DependenciesReady, PodSucceeded, etc.
	PodName         string
	PodIP           string
	NodeName        string
	Image           string
	Labels          map[string]string
}

// SandboxRuntimeStatus is the observed state of a single Sandbox CRD.
type SandboxRuntimeStatus struct {
	Name            string
	Namespace       string
	Phase           string
	PodName         string
	PodIP           string
	NodeName        string
	Image           string
	OperatingMode   string
}

// WarmPoolApplySpec is the biz-layer view of a SandboxWarmPool CRD.
type WarmPoolApplySpec struct {
	Name        string // K8s SandboxWarmPool name
	Namespace   string // K8s namespace
	TemplateRef string // SandboxTemplate K8s name
	Replicas    int32  // desired pre-warmed pod count
}

// WarmPoolSyncResult is returned by ListWarmPools for each remote WarmPool.
type WarmPoolSyncResult struct {
	Name            string
	Namespace       string
	UID             string
	ResourceVersion string
	TemplateRef     string
	Replicas        int32
	ReadyReplicas   int32
}

// SandboxClaimApplySpec is the biz-layer view of a SandboxClaim CRD.
type SandboxClaimApplySpec struct {
	Name        string // K8s SandboxClaim name
	Namespace   string // K8s namespace
	WarmPoolRef string // SandboxWarmPool K8s name
}

// SandboxTemplate is the biz-layer view of a k8s_sandbox_templates row.
type SandboxTemplate struct {
	ID                  string
	ClusterID           string
	OrgID               string
	Name                string
	DisplayName         string
	Description         string
	KubernetesName      string
	KubernetesNamespace string
	KubernetesUID       string
	ResourceVersion     string
	Image               string
	ContainerCommand    string
	Labels              map[string]string
	Status              string
	HealthMessage       string
	OwnerType           string
	OwnerID             string
	CreatedByType       string
	CreatedBy           string
	CreatedAt           time.Time
	UpdatedAt           time.Time
	Revision            int64
}

// Sandbox is the biz-layer view of a k8s_sandboxes row.
type Sandbox struct {
	ID              string
	NamespaceID     string
	ClusterID       string
	OrgID           string
	Name            string
	KubernetesName  string
	KubernetesUID   string
	ResourceVersion string
	TemplateID      string
	WarmPoolID      string
	ClaimID         string
	Lifecycle       string
	OperatingMode   string
	PodName         string
	PodIP           string
	NodeName        string
	Image           string
	WorkspacePVC    string
	NetworkMode     string
	Labels          map[string]string
	HealthMessage   string
	LastSyncAt      time.Time
	OwnerType       string
	OwnerID         string
	CreatedByType   string
	CreatedBy       string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	Revision        int64
}

// WarmPool is the biz-layer view of a k8s_sandbox_warm_pools row.
type WarmPool struct {
	ID              string
	NamespaceID     string
	ClusterID       string
	OrgID           string
	Name            string
	KubernetesName  string
	KubernetesUID   string
	ResourceVersion string
	TemplateID      string
	Replicas        int32
	ReadyReplicas   int32
	Status          string
	HealthMessage   string
	LastSyncAt      time.Time
	OwnerType       string
	OwnerID         string
	CreatedByType   string
	CreatedBy       string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	Revision        int64
}

// SandboxClaim is the biz-layer view of a k8s_sandbox_claims row.
type SandboxClaim struct {
	ID              string
	NamespaceID     string
	ClusterID       string
	OrgID           string
	Name            string
	KubernetesName  string
	KubernetesUID   string
	ResourceVersion string
	WarmPoolID      string
	SandboxID       string
	SandboxKubeName string
	SandboxPodIP    string
	Status          string
	HealthMessage   string
	LastSyncAt      time.Time
	OwnerType       string
	OwnerID         string
	CreatedByType   string
	CreatedBy       string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	Revision        int64
}

// SandboxProvider is the biz-facing view for Agent Sandbox CRD operations
// (design §11). The biz layer never touches CRD Go types directly; it asks
// the provider to apply/delete/list and trusts the data layer to use
// unstructured.Unstructured + dynamic client internally.
type SandboxProvider interface {
	// SandboxTemplate operations.
	ApplySandboxTemplate(ctx context.Context, clusterID string, locator CredentialLocator, spec SandboxTemplateApplySpec) error
	DeleteSandboxTemplate(ctx context.Context, clusterID string, locator CredentialLocator, namespace, kubeName string) error
	ListSandboxTemplates(ctx context.Context, clusterID string, locator CredentialLocator, namespace string) ([]SandboxTemplateSyncResult, error)

	// Sandbox operations.
	ApplySandbox(ctx context.Context, clusterID string, locator CredentialLocator, spec SandboxApplySpec) error
	DeleteSandbox(ctx context.Context, clusterID string, locator CredentialLocator, namespace, kubeName string) error
	ListSandboxes(ctx context.Context, clusterID string, locator CredentialLocator, namespace string) ([]SandboxSyncResult, error)
	GetSandboxStatus(ctx context.Context, clusterID string, locator CredentialLocator, namespace, kubeName string) (SandboxRuntimeStatus, error)

	// WarmPool operations.
	ApplyWarmPool(ctx context.Context, clusterID string, locator CredentialLocator, spec WarmPoolApplySpec) error
	DeleteWarmPool(ctx context.Context, clusterID string, locator CredentialLocator, namespace, kubeName string) error
	ListWarmPools(ctx context.Context, clusterID string, locator CredentialLocator, namespace string) ([]WarmPoolSyncResult, error)

	// SandboxClaim operations.
	ApplySandboxClaim(ctx context.Context, clusterID string, locator CredentialLocator, spec SandboxClaimApplySpec) error
	DeleteSandboxClaim(ctx context.Context, clusterID string, locator CredentialLocator, namespace, kubeName string) error
}

// ClusterRepository is the persistence interface for k8s_clusters (design §5.3).
// Implementations live in internal/data/cluster_repo.go. CAS methods return
// ErrClusterRevisionConflict when expected_revision mismatches; status-machine
// methods return ErrClusterNotFound when the row is missing or the expected
// status guard fails (RowsAffected == 0).
type ClusterRepository interface {
	// InTx runs fn inside a single DB transaction. The implementation injects
	// the tx into the context passed to fn so that other repos / stores that
	// share the same DB.GORM(ctx) closure (e.g. ClusterCredentialStore.Put)
	// participate in the same tx. Used by CreateCluster to atomically insert
	// the cluster row + its credential row (the credential row has a FK to
	// k8s_clusters, so both inserts must commit together). Nested InTx reuses
	// the outer tx (no-op wrapper).
	InTx(ctx context.Context, fn func(ctx context.Context) error) error

	// CreateCluster inserts a new cluster row with status=CREATING and
	// revision=1. Returns the stored row.
	CreateCluster(ctx context.Context, c *Cluster) (*Cluster, error)

	// GetCluster loads a non-deleted cluster by id. Returns ErrClusterNotFound
	// when missing or soft-deleted.
	GetCluster(ctx context.Context, id string) (*Cluster, error)

	// GetClusterByOrgName loads by (org_id, name) for the unique partial index.
	GetClusterByOrgName(ctx context.Context, orgID, name string) (*Cluster, error)

	// ListClusterCandidates scans k8s_clusters by (org_id, name > cursor)
	// ordered by (org_id, name), limit maxScan, soft-deleted excluded. This is
	// the candidate feed for ListClusters' BatchCheck authorization filter
	// (design §5.3.1 / §7.6.3). Returns clusters and the next cursor (empty
	// when exhausted).
	ListClusterCandidates(ctx context.Context, orgID, cursor string, maxScan int) ([]*Cluster, string, error)

	// UpdateClusterWithCAS applies field-masked updates guarded by
	// expected_revision. On success revision is incremented and the row
	// returned. RowsAffected==0 → ErrClusterRevisionConflict. allowedFields
	// is the caller-supplied whitelist (design §5.7.4 FieldMask); immutable
	// fields (id, org_id, credential_ref, revision, created_*) are rejected
	// by the caller before calling.
	UpdateClusterWithCAS(ctx context.Context, id string, expectedRevision int64, updates map[string]any) (*Cluster, error)

	// UpdateClusterStatus is the state-machine CAS (design §5.7.2 status
	// transitions): UPDATE WHERE id=? AND status=expected. RowsAffected==0 →
	// ErrClusterNotFound (the row is missing or not in the expected state).
	// extraUpdates lets the caller stamp probe results (cluster_uid,
	// kubernetes_version, last_probe_at, health_message) atomically with the
	// status flip.
	UpdateClusterStatus(ctx context.Context, id, expected, next string, extraUpdates map[string]any) (*Cluster, error)

	// UpdateClusterCredential stamps a new credential_ref + credential_revision
	// guarded by expected_revision (design §5.7.3 rotate step 4). Used by
	// RotateCredential after the new credential is probed.
	UpdateClusterCredential(ctx context.Context, id string, expectedRevision, newRevision int64, newRef string) (*Cluster, error)

	// SoftDeleteCluster sets deleted_at + status=DELETING/DELETED. Used by
	// DeleteCluster (design §5.7.5).
	SoftDeleteCluster(ctx context.Context, id string) error

	// CountNamespacesForCluster returns the count of non-deleted namespaces
	// on a cluster, for the DeleteCluster hard-delete guard (design §5.7.5:
	// clusters with namespaces cannot be hard-deleted → ErrFailedPrecondition).
	CountNamespacesForCluster(ctx context.Context, clusterID string) (int64, error)

	// ListClustersByOrg loads all non-deleted clusters for BatchCheck bootstrap
	// (authz_bootstrap_k8s.go). Not paginated; bounded by org size.
	ListClustersByOrg(ctx context.Context, orgID string) ([]*Cluster, error)
}

// NamespaceRepository is the persistence interface for k8s_namespaces +
// k8s_namespace_shares (design §6). CAS / status semantics mirror
// ClusterRepository.
type NamespaceRepository interface {
	CreateNamespace(ctx context.Context, ns *Namespace) (*Namespace, error)
	GetNamespace(ctx context.Context, id string) (*Namespace, error)
	GetNamespaceByClusterKubeName(ctx context.Context, clusterID, kubeName string) (*Namespace, error)
	ListNamespacesByCluster(ctx context.Context, clusterID string) ([]*Namespace, error)
	ListNamespacesByOwner(ctx context.Context, ownerType, ownerID, cursor string, maxScan int) ([]*Namespace, string, error)
	UpdateNamespaceWithCAS(ctx context.Context, id string, expectedRevision int64, updates map[string]any) (*Namespace, error)
	UpdateNamespaceVisibility(ctx context.Context, id string, expectedRevision int64, visibility, syncStatus string) (*Namespace, error)
	UpdateNamespaceStatus(ctx context.Context, id, expected, next string, extraUpdates map[string]any) (*Namespace, error)
	SoftDeleteNamespace(ctx context.Context, id string) error

	// Share CRUD (design §7.4).
	CreateShare(ctx context.Context, share *NamespaceShare) (*NamespaceShare, error)
	DeleteShare(ctx context.Context, id string) error
	ListSharesByNamespace(ctx context.Context, namespaceID string) ([]*NamespaceShare, error)

	// ListNamespacesBySyncStatus returns namespaces with a given
	// visibility_sync_status for the reconciler (design §7.5.5).
	ListNamespacesBySyncStatus(ctx context.Context, syncStatus string, limit int) ([]*Namespace, error)

	// ListSharesBySyncStatus returns shares with a given sync_status for the
	// reconciler.
	ListSharesBySyncStatus(ctx context.Context, syncStatus string, limit int) ([]*NamespaceShare, error)
}

// SandboxRepository is the persistence interface for Agent Sandbox control-
// plane records (design §11). Implementations live in internal/data/sandbox_repo.go.
// CAS Delete/Update methods return ErrClusterRevisionConflict on
// expected_revision mismatch and the entity-specific Err*NotFound sentinel
// when the row is missing or already soft-deleted.
type SandboxRepository interface {
	// SandboxTemplate CRUD
	CreateSandboxTemplate(ctx context.Context, t *SandboxTemplate) (*SandboxTemplate, error)
	GetSandboxTemplate(ctx context.Context, id string) (*SandboxTemplate, error)
	ListSandboxTemplatesByCluster(ctx context.Context, clusterID string) ([]*SandboxTemplate, error)
	DeleteSandboxTemplate(ctx context.Context, id string, expectedRevision int64) (*SandboxTemplate, error)
	UpdateSandboxTemplateStatus(ctx context.Context, id, status, healthMessage string) (*SandboxTemplate, error)

	// Sandbox CRUD
	CreateSandbox(ctx context.Context, s *Sandbox) (*Sandbox, error)
	GetSandbox(ctx context.Context, id string) (*Sandbox, error)
	ListSandboxesByNamespace(ctx context.Context, namespaceID string) ([]*Sandbox, error)
	ListSandboxesByCluster(ctx context.Context, clusterID string) ([]*Sandbox, error)
	DeleteSandbox(ctx context.Context, id string, expectedRevision int64) (*Sandbox, error)
	UpdateSandboxStatus(ctx context.Context, id string, lifecycle, healthMessage string, fields map[string]any) (*Sandbox, error)
	UpdateSandboxSync(ctx context.Context, id string, fields map[string]any) (*Sandbox, error)

	// WarmPool CRUD
	CreateWarmPool(ctx context.Context, w *WarmPool) (*WarmPool, error)
	GetWarmPool(ctx context.Context, id string) (*WarmPool, error)
	ListWarmPoolsByNamespace(ctx context.Context, namespaceID string) ([]*WarmPool, error)
	DeleteWarmPool(ctx context.Context, id string, expectedRevision int64) (*WarmPool, error)
	UpdateWarmPoolStatus(ctx context.Context, id string, status string, fields map[string]any) (*WarmPool, error)

	// SandboxClaim CRUD
	CreateSandboxClaim(ctx context.Context, c *SandboxClaim) (*SandboxClaim, error)
	GetSandboxClaim(ctx context.Context, id string) (*SandboxClaim, error)
	ListSandboxClaimsByNamespace(ctx context.Context, namespaceID string) ([]*SandboxClaim, error)
	DeleteSandboxClaim(ctx context.Context, id string, expectedRevision int64) (*SandboxClaim, error)
	UpdateSandboxClaimStatus(ctx context.Context, id string, status string, fields map[string]any) (*SandboxClaim, error)
}
