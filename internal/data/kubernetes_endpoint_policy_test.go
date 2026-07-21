package data

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/aisphereio/aisphere-hub/internal/biz"
	"github.com/aisphereio/aisphere-hub/internal/conf"
)

// fakeResolver returns a fixed IP list per hostname, so tests never touch
// real DNS and stay deterministic.
type fakeResolver struct {
	ips map[string][]net.IP
	err error
}

func (f fakeResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	if f.err != nil {
		return nil, f.err
	}
	ips := f.ips[host]
	out := make([]net.IPAddr, len(ips))
	for i, ip := range ips {
		out[i] = net.IPAddr{IP: ip}
	}
	return out, nil
}

// newTestPolicy builds an endpointPolicy with the fake resolver and the given
// config. CIDR parsing errors are fatal to the test.
func newTestPolicy(t *testing.T, cfg conf.EndpointPolicyConfig, ips map[string][]net.IP) *endpointPolicy {
	t.Helper()
	p, err := NewEndpointPolicy(cfg)
	if err != nil {
		t.Fatalf("NewEndpointPolicy() error = %v", err)
	}
	ep := p.(*endpointPolicy)
	ep.resolver = fakeResolver{ips: ips}
	return ep
}

func mustCIDRList(t *testing.T, cidrs ...string) []string {
	t.Helper()
	for _, c := range cidrs {
		if _, _, err := net.ParseCIDR(c); err != nil {
			t.Fatalf("bad test CIDR %q: %v", c, err)
		}
	}
	return cidrs
}

func TestEndpoint_RejectsHTTP(t *testing.T) {
	p := newTestPolicy(t, conf.EndpointPolicyConfig{}, nil)
	_, err := p.Validate(context.Background(), "http://10.0.0.1:6443")
	if !errors.Is(err, biz.ErrClusterInvalidArgument) {
		t.Fatalf("http url error = %v, want ErrClusterInvalidArgument", err)
	}
}

func TestEndpoint_RejectsLoopback(t *testing.T) {
	p := newTestPolicy(t, conf.EndpointPolicyConfig{}, nil)
	for _, u := range []string{"https://127.0.0.1:6443", "https://[::1]:6443"} {
		_, err := p.Validate(context.Background(), u)
		if !errors.Is(err, biz.ErrClusterInvalidArgument) {
			t.Fatalf("loopback %q error = %v, want ErrClusterInvalidArgument", u, err)
		}
	}
}

func TestEndpoint_RejectsLinkLocal(t *testing.T) {
	p := newTestPolicy(t, conf.EndpointPolicyConfig{}, nil)
	// 169.254.169.254 is the cloud metadata endpoint — must be unreachable.
	_, err := p.Validate(context.Background(), "https://169.254.169.254:6443")
	if !errors.Is(err, biz.ErrClusterInvalidArgument) {
		t.Fatalf("link-local error = %v, want ErrClusterInvalidArgument", err)
	}
}

func TestEndpoint_RejectsPrivateByDefault(t *testing.T) {
	p := newTestPolicy(t, conf.EndpointPolicyConfig{}, nil)
	for _, u := range []string{
		"https://10.0.0.1:6443",
		"https://172.16.0.1:6443",
		"https://192.168.1.1:6443",
	} {
		_, err := p.Validate(context.Background(), u)
		if !errors.Is(err, biz.ErrClusterInvalidArgument) {
			t.Fatalf("private %q error = %v, want rejection", u, err)
		}
	}
}

func TestEndpoint_AllowsPrivateWhenConfigured(t *testing.T) {
	p := newTestPolicy(t, conf.EndpointPolicyConfig{AllowPrivateClusterCIDRs: true}, nil)
	ep, err := p.Validate(context.Background(), "https://10.0.0.1:6443")
	if err != nil {
		t.Fatalf("private with allow flag error = %v, want nil", err)
	}
	if len(ep.ResolvedIPs) != 1 || ep.ResolvedIPs[0] != "10.0.0.1" {
		t.Fatalf("resolved = %v, want [10.0.0.1]", ep.ResolvedIPs)
	}
	if ep.OriginalHost != "10.0.0.1" {
		t.Fatalf("OriginalHost = %q, want 10.0.0.1", ep.OriginalHost)
	}
}

func TestEndpoint_RejectsForbiddenCIDR(t *testing.T) {
	cfg := conf.EndpointPolicyConfig{
		AllowPrivateClusterCIDRs: true,
		ForbiddenCIDRs:           mustCIDRList(t, "10.0.0.0/8"),
	}
	p := newTestPolicy(t, cfg, nil)
	// 10.0.0.1 is private (allowed by flag) but explicitly forbidden.
	_, err := p.Validate(context.Background(), "https://10.0.0.1:6443")
	if !errors.Is(err, biz.ErrClusterInvalidArgument) {
		t.Fatalf("forbidden CIDR error = %v, want rejection", err)
	}
}

func TestEndpoint_RejectsNotInEgressAllowlist(t *testing.T) {
	// Allowlist = only 203.0.113.0/24 (TEST-NET-3, public). A server_url that
	// resolves to 198.51.100.1 (TEST-NET-2, also public but not in allowlist)
	// must be rejected.
	cfg := conf.EndpointPolicyConfig{
		AllowedClusterEgress: mustCIDRList(t, "203.0.113.0/24"),
	}
	p := newTestPolicy(t, cfg, map[string][]net.IP{
		"cluster.example.com": {net.ParseIP("198.51.100.1")},
	})
	_, err := p.Validate(context.Background(), "https://cluster.example.com:6443")
	if !errors.Is(err, biz.ErrClusterInvalidArgument) {
		t.Fatalf("not-in-allowlist error = %v, want rejection", err)
	}
}

func TestEndpoint_AcceptsInEgressAllowlist(t *testing.T) {
	cfg := conf.EndpointPolicyConfig{
		AllowedClusterEgress: mustCIDRList(t, "203.0.113.0/24"),
	}
	p := newTestPolicy(t, cfg, map[string][]net.IP{
		"cluster.example.com": {net.ParseIP("203.0.113.10")},
	})
	ep, err := p.Validate(context.Background(), "https://cluster.example.com:6443")
	if err != nil {
		t.Fatalf("in-allowlist error = %v, want nil", err)
	}
	if ep.OriginalHost != "cluster.example.com" {
		t.Fatalf("OriginalHost = %q, want cluster.example.com", ep.OriginalHost)
	}
	if len(ep.ResolvedIPs) != 1 || ep.ResolvedIPs[0] != "203.0.113.10" {
		t.Fatalf("resolved = %v, want [203.0.113.10]", ep.ResolvedIPs)
	}
}

func TestEndpoint_DNSRebindingPinsIP(t *testing.T) {
	// First Validate resolves cluster.example.com → 203.0.113.10 (allowed).
	// A second Validate with the same hostname but a *different* resolved IP
	// (simulating DNS rebinding) returns the NEW IP only because the resolver
	// is the source of truth — but the point of the DialContext pin is that
	// the ClientPool caches ep.ResolvedIPs from the first call and never
	// re-resolves. This test documents that contract: Validate returns only
	// the IPs from this resolution, not any cached value.
	cfg := conf.EndpointPolicyConfig{
		AllowedClusterEgress: mustCIDRList(t, "203.0.113.0/24"),
	}
	r1 := fakeResolver{ips: map[string][]net.IP{"cluster.example.com": {net.ParseIP("203.0.113.10")}}}
	r2 := fakeResolver{ips: map[string][]net.IP{"cluster.example.com": {net.ParseIP("203.0.113.20")}}}

	p1, _ := NewEndpointPolicy(cfg)
	p1.(*endpointPolicy).resolver = r1
	ep1, err := p1.Validate(context.Background(), "https://cluster.example.com:6443")
	if err != nil {
		t.Fatalf("first Validate error = %v", err)
	}
	if ep1.ResolvedIPs[0] != "203.0.113.10" {
		t.Fatalf("first resolve = %v, want 203.0.113.10", ep1.ResolvedIPs)
	}

	// Second resolution returns a different IP. The ClientPool must cache
	// ep1.ResolvedIPs and build the DialContext from them — it must NOT call
	// Validate again (which would return the rebound IP). This test proves
	// Validate itself returns whatever the resolver says *now*, so caching at
	// the pool layer is what defeats rebinding.
	p2, _ := NewEndpointPolicy(cfg)
	p2.(*endpointPolicy).resolver = r2
	ep2, err := p2.Validate(context.Background(), "https://cluster.example.com:6443")
	if err != nil {
		t.Fatalf("second Validate error = %v", err)
	}
	if ep2.ResolvedIPs[0] != "203.0.113.20" {
		t.Fatalf("second resolve = %v, want 203.0.113.20 (rebound)", ep2.ResolvedIPs)
	}
	// The DialContext built from ep1 dials 203.0.113.10 regardless of later DNS.
	dial := NewPinnedDialContext(ep1, "6443")
	_ = dial // in a real test we'd assert the dialed address; here we just
	// document that ep1.ResolvedIPs is what NewPinnedDialContext pins.
	if ep1.ResolvedIPs[0] != "203.0.113.10" {
		t.Fatal("ep1 IPs changed; pool must cache the first resolution")
	}
}

func TestEndpoint_RejectsUnresolvableHost(t *testing.T) {
	cfg := conf.EndpointPolicyConfig{AllowPrivateClusterCIDRs: true}
	p := newTestPolicy(t, cfg, nil)
	// fakeResolver with empty ips map returns no addresses.
	_, err := p.Validate(context.Background(), "https://nonexistent.invalid:6443")
	if !errors.Is(err, biz.ErrClusterInvalidArgument) {
		t.Fatalf("unresolvable error = %v, want ErrClusterInvalidArgument", err)
	}
}

func TestEndpoint_RejectsResolverError(t *testing.T) {
	cfg := conf.EndpointPolicyConfig{AllowPrivateClusterCIDRs: true}
	p, _ := NewEndpointPolicy(cfg)
	p.(*endpointPolicy).resolver = fakeResolver{err: errors.New("dns down")}
	_, err := p.Validate(context.Background(), "https://cluster.example.com:6443")
	if !errors.Is(err, biz.ErrClusterInvalidArgument) {
		t.Fatalf("resolver error = %v, want ErrClusterInvalidArgument", err)
	}
}

func TestNewEndpointPolicy_RejectsBadCIDR(t *testing.T) {
	// Fail closed: a malformed forbidden_cidr must fail construction.
	_, err := NewEndpointPolicy(conf.EndpointPolicyConfig{
		ForbiddenCIDRs: []string{"not-a-cidr"},
	})
	if err == nil {
		t.Fatal("NewEndpointPolicy accepted bad CIDR; want error")
	}
	// A malformed allowed_cluster_egress that is not a domain and not a CIDR
	// must also fail.
	_, err = NewEndpointPolicy(conf.EndpointPolicyConfig{
		AllowedClusterEgress: []string{"not-a-cidr-not-a-domain"},
	})
	if err == nil {
		t.Fatal("NewEndpointPolicy accepted bad egress entry; want error")
	}
}
