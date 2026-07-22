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
	"k8s.io/client-go/tools/clientcmd"

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
// SSRF: every externally supplied API server URL is validated through
// EndpointPolicy and its DialContext is pinned to the validated IP set. For a
// kubeconfig, the selected context is first compiled into rest.Config, then the
// resulting Host is validated and pinned. Proxy URLs, exec/auth-provider
// plugins, and impersonation are rejected because they would bypass the Hub
// trust boundary or execute caller-controlled code.
type k8sClientPool struct {
	store   biz.ClusterCredentialStore
	policy  biz.EndpointPolicy
	factory clientFactory
	logger  logx.Logger

	clients sync.Map
	inflight singleflight.Group
}

type clientEntry struct {
	client   kubernetesx.Client
	revision int64
}

type clientFactory func(ctx context.Context, clusterID string, locator biz.CredentialLocator, cred kubernetesx.Credential) (kubernetesx.Client, error)

func NewClientPool(store biz.ClusterCredentialStore, policy biz.EndpointPolicy, logger logx.Logger) *k8sClientPool {
	return &k8sClientPool{
		store:   store,
		policy:  policy,
		factory: defaultClientFactory(policy, logger),
		logger:  logger,
	}
}

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

		switch cred.Kind {
		case kubernetesx.CredentialKindKubeconfig:
			restCfg, err := restConfigFromKubeconfig(cred)
			if err != nil {
				return nil, fmt.Errorf("compile kubeconfig for cluster %s: %w", clusterID, err)
			}
			if err := validateKubeconfigRestConfig(restCfg); err != nil {
				return nil, fmt.Errorf("unsafe kubeconfig for cluster %s: %w", clusterID, err)
			}
			if err := pinRESTConfig(ctx, policy, restCfg); err != nil {
				return nil, err
			}
			restCfg.QPS = 50
			restCfg.Burst = 100
			return kubernetesx.New(merged, kubernetesx.WithRESTConfig(restCfg))

		case kubernetesx.CredentialKindServiceAccount:
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

		case kubernetesx.CredentialKindInCluster:
			// In-cluster credentials are resolved from the pod service account and
			// target the local cluster service; there is no caller-controlled URL.
			return kubernetesx.New(merged)

		default:
			return nil, kubernetesx.ErrCredentialInvalid
		}
	}
}

func restConfigFromKubeconfig(cred kubernetesx.Credential) (*rest.Config, error) {
	raw, err := clientcmd.Load(cred.Kubeconfig)
	if err != nil {
		return nil, err
	}
	contextName := cred.Context
	if contextName == "" {
		contextName = raw.CurrentContext
	}
	if contextName == "" {
		return nil, errors.New("kubeconfig current-context is empty")
	}
	return clientcmd.NewNonInteractiveClientConfig(*raw, contextName, &clientcmd.ConfigOverrides{}, nil).ClientConfig()
}

func validateKubeconfigRestConfig(cfg *rest.Config) error {
	if cfg == nil {
		return errors.New("rest config is nil")
	}
	if cfg.Proxy != nil {
		return errors.New("proxy-url is not allowed")
	}
	if cfg.ExecProvider != nil {
		return errors.New("exec credential plugins are not allowed")
	}
	if cfg.AuthProvider != nil {
		return errors.New("auth-provider plugins are not allowed")
	}
	if cfg.Impersonate.UserName != "" || cfg.Impersonate.UID != "" || len(cfg.Impersonate.Groups) > 0 || len(cfg.Impersonate.Extra) > 0 {
		return errors.New("impersonation is not allowed")
	}
	return nil
}

func pinRESTConfig(ctx context.Context, policy biz.EndpointPolicy, cfg *rest.Config) error {
	resolved, err := policy.Validate(ctx, cfg.Host)
	if err != nil {
		return err
	}
	port := portFromURL(cfg.Host)
	if port == "" {
		return errors.New("client pool: kubeconfig server URL has no port")
	}
	cfg.Dial = NewPinnedDialContext(resolved, port)
	if cfg.TLSClientConfig.ServerName == "" {
		cfg.TLSClientConfig.ServerName = resolved.OriginalHost
	}
	return nil
}

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

func (p *k8sClientPool) getOrBuild(ctx context.Context, clusterID string, locator biz.CredentialLocator, cred kubernetesx.Credential) (kubernetesx.Client, error) {
	if v, ok := p.clients.Load(clusterID); ok {
		entry := v.(*clientEntry)
		if entry.revision == locator.CredentialRevision {
			return entry.client, nil
		}
	}
	key := fmt.Sprintf("%s@%d", clusterID, locator.CredentialRevision)
	v, err, _ := p.inflight.Do(key, func() (interface{}, error) {
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

func (p *k8sClientPool) Probe(ctx context.Context, clusterID string, locator biz.CredentialLocator, cred kubernetesx.Credential) (kubernetesx.ProbeResult, error) {
	client, err := p.getOrBuild(ctx, clusterID, locator, cred)
	if err != nil {
		return kubernetesx.ProbeResult{}, err
	}
	return client.Probe(ctx, kubernetesx.ProbeRequest{})
}

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

func (p *k8sClientPool) DeleteNamespace(ctx context.Context, clusterID string, locator biz.CredentialLocator, kubeName string) error {
	client, err := p.getOrBuild(ctx, clusterID, locator, kubernetesx.Credential{})
	if err != nil {
		return err
	}
	return client.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: kubeName}})
}

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

func (p *k8sClientPool) InvalidateCluster(ctx context.Context, clusterID string) {
	p.clients.Delete(clusterID)
}

func (p *k8sClientPool) Close() error {
	p.clients.Range(func(k, _ interface{}) bool {
		p.clients.Delete(k)
		return true
	})
	return nil
}

var _ biz.KubernetesProvider = (*k8sClientPool)(nil)

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
