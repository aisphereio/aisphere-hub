package data

import (
	"context"
	"crypto/tls"
	"fmt"
	"time"

	"github.com/aisphereio/kernel/cachex"
	"github.com/aisphereio/kernel/taskx"
	"github.com/redis/go-redis/v9"
)

// NewKubernetesTaskLocker builds the Redis-backed taskx locker used by the
// Kubernetes reconciliation jobs. It deliberately owns a dedicated Redis
// client instead of trying to unwrap cachex.Cache (whose driver client is an
// implementation detail). The returned close function must be called during
// process shutdown.
func NewKubernetesTaskLocker(ctx context.Context, cfg cachex.Config) (taskx.Locker, func() error, error) {
	if err := cfg.Validate(); err != nil {
		return nil, nil, fmt.Errorf("kubernetes task locker: invalid redis config: %w", err)
	}

	client := newUniversalRedisClient(cfg)
	pingCtx, cancel := context.WithTimeout(ctx, durationOrDefault(cfg.DialTimeout, 5*time.Second))
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, nil, fmt.Errorf("kubernetes task locker: ping redis: %w", err)
	}

	prefix := cfg.KeyPrefix
	if prefix != "" {
		prefix += ":"
	}
	prefix += "taskx:lease:"
	return taskx.NewRedisLocker(client, prefix), client.Close, nil
}

func newUniversalRedisClient(cfg cachex.Config) redis.UniversalClient {
	tlsConfig := redisTLSConfig(cfg)
	if cfg.Cluster {
		return redis.NewClusterClient(&redis.ClusterOptions{
			Addrs:        cfg.Addrs,
			Username:     cfg.Username,
			Password:     cfg.Password,
			PoolSize:     cfg.PoolSize,
			MinIdleConns: cfg.MinIdleConns,
			DialTimeout:  durationOrDefault(cfg.DialTimeout, 5*time.Second),
			ReadTimeout:  durationOrDefault(cfg.ReadTimeout, 3*time.Second),
			WriteTimeout: durationOrDefault(cfg.WriteTimeout, 3*time.Second),
			TLSConfig:    tlsConfig,
		})
	}
	if cfg.MasterName != "" {
		return redis.NewFailoverClient(&redis.FailoverOptions{
			MasterName:    cfg.MasterName,
			SentinelAddrs: cfg.Addrs,
			Username:      cfg.Username,
			Password:      cfg.Password,
			DB:            cfg.DB,
			PoolSize:      cfg.PoolSize,
			MinIdleConns:  cfg.MinIdleConns,
			DialTimeout:   durationOrDefault(cfg.DialTimeout, 5*time.Second),
			ReadTimeout:   durationOrDefault(cfg.ReadTimeout, 3*time.Second),
			WriteTimeout:  durationOrDefault(cfg.WriteTimeout, 3*time.Second),
			TLSConfig:     tlsConfig,
		})
	}
	return redis.NewClient(&redis.Options{
		Addr:         cfg.Addrs[0],
		Username:     cfg.Username,
		Password:     cfg.Password,
		DB:           cfg.DB,
		PoolSize:     cfg.PoolSize,
		MinIdleConns: cfg.MinIdleConns,
		DialTimeout:  durationOrDefault(cfg.DialTimeout, 5*time.Second),
		ReadTimeout:  durationOrDefault(cfg.ReadTimeout, 3*time.Second),
		WriteTimeout: durationOrDefault(cfg.WriteTimeout, 3*time.Second),
		TLSConfig:    tlsConfig,
	})
}

func redisTLSConfig(cfg cachex.Config) *tls.Config {
	if !cfg.TLSEnabled {
		return nil
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: cfg.TLSSkipVerify} //nolint:gosec // explicitly controlled by deployment config
}

func durationOrDefault(value, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}
