package registry

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aisphereio/kernel/gatewayx"
	clientv3 "go.etcd.io/etcd/client/v3"
)

type Config struct {
	Provider       string        `json:"provider" yaml:"provider"`
	Prefix         string        `json:"prefix" yaml:"prefix"`
	Endpoints      []string      `json:"endpoints" yaml:"endpoints"`
	DialTimeout    time.Duration `json:"dial_timeout_ns" yaml:"dial_timeout_ns"`
	RequestTimeout time.Duration `json:"request_timeout_ns" yaml:"request_timeout_ns"`
}

func NewRouteRegistry(ctx context.Context, cfg Config) (gatewayx.RouteRegistry, func(), error) {
	provider := strings.ToLower(strings.TrimSpace(cfg.Provider))
	switch provider {
	case "", "memory":
		return gatewayx.NewMemoryRegistry(), func() {}, nil
	case "etcd":
		if len(cfg.Endpoints) == 0 {
			return nil, nil, fmt.Errorf("gateway route registry etcd endpoints are required")
		}
		dialTimeout := cfg.DialTimeout
		if dialTimeout <= 0 {
			dialTimeout = 3 * time.Second
		}
		client, err := clientv3.New(clientv3.Config{
			Context:     ctx,
			Endpoints:   cfg.Endpoints,
			DialTimeout: dialTimeout,
		})
		if err != nil {
			return nil, nil, err
		}
		store := &EtcdKVStore{Client: client, RequestTimeout: cfg.RequestTimeout}
		return gatewayx.NewEtcdRegistry(store, cfg.Prefix), func() { _ = client.Close() }, nil
	default:
		return nil, nil, fmt.Errorf("unsupported gateway route registry provider %q", cfg.Provider)
	}
}

type EtcdKVStore struct {
	Client         *clientv3.Client
	RequestTimeout time.Duration
}

func (s *EtcdKVStore) Put(ctx context.Context, key string, value []byte) error {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	_, err := s.Client.Put(ctx, key, string(value))
	return err
}

func (s *EtcdKVStore) DeletePrefix(ctx context.Context, prefix string) error {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	_, err := s.Client.Delete(ctx, prefix, clientv3.WithPrefix())
	return err
}

func (s *EtcdKVStore) ListPrefix(ctx context.Context, prefix string) (map[string][]byte, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	resp, err := s.Client.Get(ctx, prefix, clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	out := make(map[string][]byte, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		out[string(kv.Key)] = append([]byte(nil), kv.Value...)
	}
	return out, nil
}

func (s *EtcdKVStore) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := s.RequestTimeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	return context.WithTimeout(ctx, timeout)
}
