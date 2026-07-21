package conf

import (
	"fmt"
	"strings"
	"time"

	"github.com/aisphereio/aisphere-iam/client/authzgrpc"
	"github.com/aisphereio/kernel/accessx"
	"github.com/aisphereio/kernel/authn"
	"github.com/aisphereio/kernel/authn/casdoor"
	"github.com/aisphereio/kernel/authn/oidcx"
	"github.com/aisphereio/kernel/cachex"
	"github.com/aisphereio/kernel/dbx"
	"github.com/aisphereio/kernel/dtmx"
	"github.com/aisphereio/kernel/logx"
	"github.com/aisphereio/kernel/migrationx"
	"github.com/aisphereio/kernel/objectstorex"
)

type Bootstrap struct {
	Service    ServiceConfig    `json:"service" yaml:"service"`
	Server     ServerConfig     `json:"server" yaml:"server"`
	Log        logx.Config      `json:"log" yaml:"log"`
	Data       DataConfig       `json:"data" yaml:"data"`
	Security   SecurityConfig   `json:"security" yaml:"security"`
	Gateway    GatewayConfig    `json:"gateway" yaml:"gateway"`
	Audit      AuditConfig      `json:"audit" yaml:"audit"`
	Metrics    MetricsConfig    `json:"metrics" yaml:"metrics"`
	DTM        dtmx.Config      `json:"dtm" yaml:"dtm"`
	Skill      SkillConfig      `json:"skill" yaml:"skill"`
	Kubernetes KubernetesConfig `json:"kubernetes" yaml:"kubernetes"`
}

type ServiceConfig struct {
	Name    string `json:"name" yaml:"name"`
	Version string `json:"version" yaml:"version"`
	Env     string `json:"env" yaml:"env"`
}

type ServerConfig struct {
	HTTP HTTPConfig `json:"http" yaml:"http"`
	GRPC GRPCConfig `json:"grpc" yaml:"grpc"`
}

type HTTPConfig struct {
	Addr    string        `json:"addr" yaml:"addr"`
	Timeout time.Duration `json:"timeout_ns" yaml:"timeout_ns"`
	CORS    CORSConfig    `json:"cors" yaml:"cors"`
}

type CORSConfig struct {
	Enabled          bool          `json:"enabled" yaml:"enabled"`
	AllowedOrigins   []string      `json:"allowed_origins" yaml:"allowed_origins"`
	AllowedMethods   []string      `json:"allowed_methods" yaml:"allowed_methods"`
	AllowedHeaders   []string      `json:"allowed_headers" yaml:"allowed_headers"`
	ExposedHeaders   []string      `json:"exposed_headers" yaml:"exposed_headers"`
	AllowCredentials bool          `json:"allow_credentials" yaml:"allow_credentials"`
	MaxAge           time.Duration `json:"max_age_ns" yaml:"max_age_ns"`
}

type GRPCConfig struct {
	Addr    string        `json:"addr" yaml:"addr"`
	Timeout time.Duration `json:"timeout_ns" yaml:"timeout_ns"`
}

type DataConfig struct {
	Database    DatabaseConfig    `json:"database" yaml:"database"`
	Cache       CacheConfig       `json:"cache" yaml:"cache"`
	ObjectStore ObjectStoreConfig `json:"object_store" yaml:"object_store"`
}

type DatabaseConfig struct {
	Enabled   bool              `json:"enabled" yaml:"enabled"`
	Config    dbx.Config        `json:"config" yaml:"config"`
	Migration migrationx.Config `json:"migration" yaml:"migration"`
}

type CacheConfig struct {
	Enabled bool          `json:"enabled" yaml:"enabled"`
	Config  cachex.Config `json:"config" yaml:"config"`
}

type ObjectStoreConfig struct {
	Enabled bool                `json:"enabled" yaml:"enabled"`
	Config  objectstorex.Config `json:"config" yaml:"config"`
}

type SecurityConfig struct {
	Authn        AuthnConfig                      `json:"authn" yaml:"authn"`
	Authz        AuthzConfig                      `json:"authz" yaml:"authz"`
	Access       accessx.AccessConfig             `json:"access" yaml:"access"`
	InternalCall authn.InternalServiceTokenConfig `json:"internal_call" yaml:"internal_call"`
}

type AuthnConfig struct {
	Enabled      bool                     `json:"enabled" yaml:"enabled"`
	Provider     string                   `json:"provider" yaml:"provider"`
	Mode         string                   `json:"mode" yaml:"mode"`
	PrincipalJWT authn.PrincipalJWTConfig `json:"principal_jwt" yaml:"principal_jwt"`
	OIDC         oidcx.Config             `json:"oidc" yaml:"oidc"`
	Casdoor      casdoor.Config           `json:"casdoor" yaml:"casdoor"`
	CacheTTL     time.Duration            `json:"cache_ttl_ns" yaml:"cache_ttl_ns"`
}

type AuthzConfig struct {
	Enabled     bool             `json:"enabled" yaml:"enabled"`
	Provider    string           `json:"provider" yaml:"provider"`
	DevAllowAll bool             `json:"dev_allow_all" yaml:"dev_allow_all"`
	IAMGRPC     authzgrpc.Config `json:"iam_grpc" yaml:"iam_grpc"`
}

func ValidateProductionSecurity(service ServiceConfig, security SecurityConfig) error {
	env := strings.ToLower(strings.TrimSpace(service.Env))
	if env != "production" && env != "prod" {
		return nil
	}
	if !security.Authn.Enabled {
		return fmt.Errorf("production security requires authn")
	}
	if strings.ToLower(strings.TrimSpace(security.Authn.Mode)) != "principal_jwt" {
		return fmt.Errorf("production security requires authn mode principal_jwt")
	}
	if strings.TrimSpace(security.Authn.PrincipalJWT.Secret) == "" {
		return fmt.Errorf("production security requires principal_jwt secret")
	}
	if !security.Authz.Enabled || security.Authz.DevAllowAll {
		return fmt.Errorf("production security requires fail-closed authz")
	}
	if strings.ToLower(strings.TrimSpace(security.Authz.Provider)) != "iam_grpc" {
		return fmt.Errorf("production security requires IAM gRPC authz provider")
	}
	return nil
}

type GatewayConfig struct {
	RouteRegistry RouteRegistryConfig `json:"route_registry" yaml:"route_registry"`
}

type RouteRegistryConfig struct {
	Provider       string        `json:"provider" yaml:"provider"`
	Prefix         string        `json:"prefix" yaml:"prefix"`
	Endpoints      []string      `json:"endpoints" yaml:"endpoints"`
	DialTimeout    time.Duration `json:"dial_timeout_ns" yaml:"dial_timeout_ns"`
	RequestTimeout time.Duration `json:"request_timeout_ns" yaml:"request_timeout_ns"`
}

type AuditConfig struct {
	Enabled bool   `json:"enabled" yaml:"enabled"`
	Store   string `json:"store" yaml:"store"`
}

type MetricsConfig struct {
	Enabled bool   `json:"enabled" yaml:"enabled"`
	Addr    string `json:"addr" yaml:"addr"`
	Path    string `json:"path" yaml:"path"`
	Pprof   bool   `json:"pprof" yaml:"pprof"`
}

// SkillConfig controls the embedded Git service used by native Skill repos.
type SkillConfig struct {
	Git SkillGitConfig `json:"git" yaml:"git"`
}

type SkillGitConfig struct {
	DataPath string `json:"data_path" yaml:"data_path"`
}

// KubernetesConfig controls the Kubernetes cluster management plane
// (design §5/§12.4). When Enabled is false, the Cluster/Namespace services
// are not wired and the migration tables remain unused. Master keys are
// injected via env (configenv overlay); yaml only carries placeholders so
// secrets never land in version control.
type KubernetesConfig struct {
	Enabled    bool                 `json:"enabled" yaml:"enabled"`
	Encryption EncryptionConfig     `json:"encryption" yaml:"encryption"`
	Endpoint   EndpointPolicyConfig `json:"endpoint" yaml:"endpoint"`
	ClientPool ClientPoolConfig     `json:"client_pool" yaml:"client_pool"`
	Reconcile  ReconcileConfig      `json:"reconcile" yaml:"reconcile"`
}

// EncryptionConfig holds versioned AEAD master keys (design §5.5 V1: no
// KMS/Vault). MasterKeys maps key_version -> base64-encoded 32-byte AES key.
// ActiveVersion is the version used for new Put calls. RotateKey re-encrypts
// rows from an old version to ActiveVersion.
type EncryptionConfig struct {
	MasterKeys    map[string]string `json:"master_keys" yaml:"master_keys"`
	ActiveVersion string            `json:"active_version" yaml:"active_version"`
}

// EndpointPolicyConfig is the Hub SSRF guard configuration (design §12.4).
// ForbiddenCIDRs rejects loopback/link-local/private by default; setting
// AllowPrivateClusterCIDRs true permits private CIDRs (for on-prem clusters).
type EndpointPolicyConfig struct {
	AllowedClusterEgress     []string `json:"allowed_cluster_egress" yaml:"allowed_cluster_egress"`
	ForbiddenCIDRs           []string `json:"forbidden_cidrs" yaml:"forbidden_cidrs"`
	AllowPrivateClusterCIDRs bool     `json:"allow_private_cluster_cidrs" yaml:"allow_private_cluster_cidrs"`
	AllowInsecureDev         bool     `json:"allow_insecure_dev" yaml:"allow_insecure_dev"`
}

// ClientPoolConfig bounds the kubernetesx.Client cache (design §5.6).
type ClientPoolConfig struct {
	MaxActiveClients int           `json:"max_active_clients" yaml:"max_active_clients"`
	TTL              time.Duration `json:"ttl_ns" yaml:"ttl_ns"`
}

// ReconcileConfig drives the taskx.Scheduler visibility reconciler and
// bounds the List request hydration/refill loops (design §7.5.5/§7.6.1).
type ReconcileConfig struct {
	Interval         time.Duration `json:"interval_ns" yaml:"interval_ns"`
	MaxHydrateRounds int           `json:"max_hydrate_rounds" yaml:"max_hydrate_rounds"`
	MaxScan          int           `json:"max_scan" yaml:"max_scan"`
	LeaseTTL         time.Duration `json:"lease_ttl_ns" yaml:"lease_ttl_ns"`
}
