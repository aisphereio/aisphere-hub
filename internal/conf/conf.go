package conf

import (
	"time"

	"github.com/aisphereio/kernel/authn/casdoor"
	"github.com/aisphereio/kernel/authz/spicedb"
	"github.com/aisphereio/kernel/cachex"
	"github.com/aisphereio/kernel/dbx"
	"github.com/aisphereio/kernel/dtmx"
	"github.com/aisphereio/kernel/logx"
	"github.com/aisphereio/kernel/objectstorex"
)

type Bootstrap struct {
	Service  ServiceConfig  `json:"service" yaml:"service"`
	Server   ServerConfig   `json:"server" yaml:"server"`
	Log      logx.Config    `json:"log" yaml:"log"`
	Data     DataConfig     `json:"data" yaml:"data"`
	Security SecurityConfig `json:"security" yaml:"security"`
	Audit    AuditConfig    `json:"audit" yaml:"audit"`
	Metrics  MetricsConfig  `json:"metrics" yaml:"metrics"`
	DTM      dtmx.Config    `json:"dtm" yaml:"dtm"`
	Skill    SkillConfig    `json:"skill" yaml:"skill"`
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
	Enabled bool       `json:"enabled" yaml:"enabled"`
	Config  dbx.Config `json:"config" yaml:"config"`
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
	Authn AuthnConfig `json:"authn" yaml:"authn"`
	Authz AuthzConfig `json:"authz" yaml:"authz"`
}

type AuthnConfig struct {
	Enabled  bool           `json:"enabled" yaml:"enabled"`
	Provider string         `json:"provider" yaml:"provider"`
	Casdoor  casdoor.Config `json:"casdoor" yaml:"casdoor"`
}

type AuthzConfig struct {
	Enabled     bool           `json:"enabled" yaml:"enabled"`
	Provider    string         `json:"provider" yaml:"provider"`
	DevAllowAll bool           `json:"dev_allow_all" yaml:"dev_allow_all"`
	SpiceDB     spicedb.Config `json:"spicedb" yaml:"spicedb"`
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

// SkillConfig controls skill package storage behavior.
type SkillConfig struct {
	Storage SkillStorageConfig `json:"storage" yaml:"storage"`
}

// SkillStorageConfig controls S3-first skill package persistence.
type SkillStorageConfig struct {
	// MaxVersions is the maximum number of versions to keep per skill.
	// Zero means keep all versions. Cleanup is best-effort and runs after a
	// successful PG metadata commit.
	MaxVersions int `json:"max_versions" yaml:"max_versions"`
}
