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
// allocated internally by ClusterCredentialStore.Put; the biz layer never
// invents refs. AAD is reconstructed by the Store from {ClusterID, Ref,
// CredentialRevision} — callers never supply AAD (design §5.5, avoids the
// circular dependency where the caller would have to know the ref before Put
// allocates it).
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
	// Put encrypts value under a freshly allocated ref, using
	// {clusterID, newRef, credentialRevision} as AAD. Returns the full
	// Locator so the biz layer can persist credential_ref + credential_revision
	// in the k8s_clusters row. credentialRevision is the *target* revision
	// (Put does not increment it; the caller chooses 1 for create, current+1
	// for rotate — design §5.5).
	Put(ctx context.Context, clusterID string, credentialRevision int64, value kubernetesx.Credential) (CredentialLocator, error)

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
