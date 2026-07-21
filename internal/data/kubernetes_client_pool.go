package data

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync"

	"golang.org/x/sync/singleflight"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	"github.com/aisphereio/aisphere-hub/internal/biz"
	"github.com/aisphereio/kernel/kubernetesx"
	"github.com/aisphereio/kernel/logx"
)

// k8sClientPool implements biz.KubernetesProvider (design §5.6). It caches one
// kubernetesx.Client per cluster_id, keyed by credential_revision so a rotate
// invalidates the stale entry on the next request. Concurrent builds for the
// same cluster_id are deduped via singleflight (design §5.6 "ClientPool"). The
// pool never stores credentials: on a cache miss it resolves the credential
// from the ClusterCredentialStore (or accepts one from the caller, as Probe
// does) and builds a fresh client via the injected factory.
//
// SSRF: the factory validates the server_url through EndpointPolicy and, for
// ServiceAccount/InCluster credentials, pins the DialContext to the resolved
// IPs (design §12.4 DNS rebinding defense). Kubeconfig credentials carry their
// server URL inside the YAML; V1 trusts the CreateCluster-time validation and
// relies on client-go's default dialer for them — pinning kubeconfig is a V2
// follow-up noted in the design.
type k8sClientPool struct {
	store   biz.ClusterCredentialStore
	policy  biz.EndpointPolicy
	factory clientFactory
	logger  logx.Logger

	// clients maps cluster_id → *clientEntry. We store *clientEntry so a
	// LoadOrStore compare-and-swap can detect a concurrent winner without
	// holding a global lock. Entries are immutable once published: a rotate
	// publishes a new *clientEntry (new revision) rather than mutating the
	// old one, so in-flight requests holding the old pointer finish safely.
	clients sync.Map

	// inflight dedupes concurrent builds for the same cluster_id so two
	// requests hitting a cold cache share one factory call.
	inflight singleflight.Group
}

// clientEntry is the cached value. revision is the credential_revision the
// client was built from; a mismatch with the locator forces a rebuild.
type clientEntry struct {
	client   kubernetesx.Client
	revision int64
}

// clientFactory builds a kubernetesx.Client from a credential + locator. It is
// the only seam that touches kubernetesx.New, so tests inject a fake to verify
// cache behavior without standing up a real API server. The production factory
// (defaultClientFactory) runs SSRF validation + pinned dial.
type clientFactory func(ctx context.Context, clusterID string, locator biz.CredentialLocator, cred kubernetesx.Credential) (kubernetesx.Client, error)

// NewClientPool builds the pool with the production factory. The factory wires
// EndpointPolicy + pinned DialContext; callers (Resources) supply the store,
// policy, and logger obtained from NewCredentialStore / NewEndpointPolicy.
func NewClientPool(store biz.ClusterCredentialStore, policy biz.EndpointPolicy, logger logx.Logger) *k8sClientPool {
	return &k8sClientPool{
		store:   store,
		policy:  policy,
		factory: defaultClientFactory(policy, logger),
		logger:  logger,
	}
}

// defaultClientFactory is the production clientFactory. It merges the credential
// into a kubernetesx.Config, runs the SSRF guard, and — for ServiceAccount /
// InCluster credentials — injects a pinned DialContext via WithRESTConfig so
// the TCP connection dials the resolved IP directly (design §12.4).
func defaultClientFactory(policy biz.EndpointPolicy, logger logx.Logger) clientFactory {
	return func(ctx context.Context, clusterID string, locator biz.CredentialLocator, cred kubernetesx.Credential) (kubernetesx.Client, error) {
		base := kubernetesx.Config{
			FieldManager: "aisphere-hub",
			QPS:          50,
			Burst:        100,
			Logger:       logger,
		}
		merged, err := base.MergeCredential(cred)
		if err != nil {
			return nil, fmt.Errorf("merge credential for cluster %s: %w", clusterID, err)
		}
		// SSRF + pinned dial only for Host-based credentials. Kubeconfig
		// carries its server URL inside the YAML; V1 relies on the
		// CreateCluster-time EndpointPolicy check (PR③ biz layer) and uses
		// client-go's default resolver for the dial.
		if cred.Kind == kubernetesx.CredentialKindServiceAccount || cred.Kind == kubernetesx.CredentialKindInCluster {
			resolved, err := policy.Validate(ctx, cred.Host)
			if err != nil {
				return nil, err
			}
			port := portFromURL(cred.Host)
			if port == "" {
				return nil, errors.New("client pool: server_url has no port")
			}
			restCfg := &rest.Config{
				Host: merged.Host,
				TLSClientConfig: rest.TLSClientConfig{
					CAData:     cred.CACert,
					ServerName: resolved.OriginalHost,
				},
				BearerToken: cred.Token,
				Dial:        NewPinnedDialContext(resolved, port),
				QPS:         50,
				Burst:       100,
			}
			return kubernetesx.New(merged, kubernetesx.WithRESTConfig(restCfg))
		}
		return kubernetesx.New(merged)
	}
}

// portFromURL extracts the port from a URL string, falling back to "443" for
// https URLs without an explicit port (rare for kube API servers but defensive).
func portFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Port() == "" {
		if u != nil && u.Scheme == "https" {
			return "443"
		}
		return ""
	}
	return u.Port()
}

// getOrBuild returns a cached client whose revision matches locator, or builds
// one via singleflight. cred is optional: when non-zero (Kind != ""), it is
// used directly; otherwise the pool loads the credential from the store. The
// factory is called at most once per concurrent cold-cache miss per cluster.
func (p *k8sClientPool) getOrBuild(ctx context.Context, clusterID string, locator biz.CredentialLocator, cred kubernetesx.Credential) (kubernetesx.Client, error) {
	if v, ok := p.clients.Load(clusterID); ok {
		entry := v.(*clientEntry)
		if entry.revision == locator.CredentialRevision {
			return entry.client, nil
		}
		// Revision mismatch (rotate happened): fall through to rebuild. The
		// stale entry stays until the new build publishes, then is replaced.
	}
	// singleflight key includes revision so a concurrent rotate does not
	// collapse two different revisions into one build.
	key := fmt.Sprintf("%s@%d", clusterID, locator.CredentialRevision)
	v, err, _ := p.inflight.Do(key, func() (interface{}, error) {
		// Re-check under the singleflight winner: another request may have
		// just published the entry we want.
		if v, ok := p.clients.Load(clusterID); ok {
			entry := v.(*clientEntry)
			if entry.revision == locator.CredentialRevision {
				return entry.client, nil
			}
		}
		c := cred
		if c.Kind == "" {
			loaded, lerr := p.store.Get(ctx, locator)
			if lerr != nil {
				return nil, fmt.Errorf("load credential for cluster %s: %w", clusterID, lerr)
			}
			c = loaded
		}
		client, ferr := p.factory(ctx, clusterID, locator, c)
		if ferr != nil {
			return nil, ferr
		}
		p.clients.Store(clusterID, &clientEntry{client: client, revision: locator.CredentialRevision})
		return client, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(kubernetesx.Client), nil
}

// Probe runs a reachability probe via the cached client (design §5.7.6). The
// biz layer passes the credential explicitly because RotateCredential probes a
// not-yet-committed revision (design §5.7.3 step 3); the pool still caches by
// revision so a successful rotate reuses the probed client.
func (p *k8sClientPool) Probe(ctx context.Context, clusterID string, locator biz.CredentialLocator, cred kubernetesx.Credential) (kubernetesx.ProbeResult, error) {
	client, err := p.getOrBuild(ctx, clusterID, locator, cred)
	if err != nil {
		return kubernetesx.ProbeResult{}, err
	}
	return client.Probe(ctx, kubernetesx.ProbeRequest{})
}

// ApplyNamespace SSA-applies a Namespace on the cluster (design §6.4 step 6).
// The data layer injects aisphere.io/* managed labels here before delegating.
func (p *k8sClientPool) ApplyNamespace(ctx context.Context, clusterID string, locator biz.CredentialLocator, ns biz.NamespaceApplySpec) error {
	client, err := p.getOrBuild(ctx, clusterID, locator, kubernetesx.Credential{})
	if err != nil {
		return err
	}
	target := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: ns.Name, Labels: ns.Labels, Annotations: ns.Annotations},
	}
	return client.Apply(ctx, target, kubernetesx.ApplyOptions{FieldManager: "aisphere-hub-namespace"})
}

// DeleteNamespace removes a remote Namespace by kube_name (design §6.6).
func (p *k8sClientPool) DeleteNamespace(ctx context.Context, clusterID string, locator biz.CredentialLocator, kubeName string) error {
	client, err := p.getOrBuild(ctx, clusterID, locator, kubernetesx.Credential{})
	if err != nil {
		return err
	}
	return client.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: kubeName}})
}

// ListNamespaces enumerates remote Namespaces for SyncNamespaces.
func (p *k8sClientPool) ListNamespaces(ctx context.Context, clusterID string, locator biz.CredentialLocator) ([]biz.NamespaceSyncResult, error) {
	client, err := p.getOrBuild(ctx, clusterID, locator, kubernetesx.Credential{})
	if err != nil {
		return nil, err
	}
	list := &corev1.NamespaceList{}
	if err := client.List(ctx, list); err != nil {
		return nil, err
	}
	out := make([]biz.NamespaceSyncResult, 0, len(list.Items))
	for _, item := range list.Items {
		out = append(out, biz.NamespaceSyncResult{
			Name:            item.Name,
			UID:             string(item.UID),
			ResourceVersion: item.ResourceVersion,
			Labels:          item.Labels,
		})
	}
	return out, nil
}

// InvalidateCluster drops the cached client after a credential rotate (design
// §5.7.3 step 5) or cluster delete. The next request rebuilds from the new
// credential. kubernetesx.Client has no Close hook (controller-runtime clients
// release via GC), so we just drop the reference.
func (p *k8sClientPool) InvalidateCluster(ctx context.Context, clusterID string) {
	p.clients.Delete(clusterID)
}

// Close empties the cache at shutdown. Registered with Resources.closers.
func (p *k8sClientPool) Close() error {
	p.clients.Range(func(k, _ interface{}) bool {
		p.clients.Delete(k)
		return true
	})
	return nil
}

// compile-time interface check.
var _ biz.KubernetesProvider = (*k8sClientPool)(nil)

// noClientPool is a no-op implementation returned when Kubernetes is disabled.
// Every method returns a sentinel error so a misconfigured call fails loudly
// rather than silently dropping work.
type noClientPool struct{}

func (noClientPool) Probe(context.Context, string, biz.CredentialLocator, kubernetesx.Credential) (kubernetesx.ProbeResult, error) {
	return kubernetesx.ProbeResult{}, errors.New("kubernetes provider disabled: kubernetes.enabled is false")
}
func (noClientPool) ApplyNamespace(context.Context, string, biz.CredentialLocator, biz.NamespaceApplySpec) error {
	return errors.New("kubernetes provider disabled")
}
func (noClientPool) DeleteNamespace(context.Context, string, biz.CredentialLocator, string) error {
	return errors.New("kubernetes provider disabled")
}
func (noClientPool) ListNamespaces(context.Context, string, biz.CredentialLocator) ([]biz.NamespaceSyncResult, error) {
	return nil, errors.New("kubernetes provider disabled")
}
func (noClientPool) InvalidateCluster(context.Context, string) {}

var _ biz.KubernetesProvider = noClientPool{}
