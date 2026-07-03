package conf

import (
	"os"
	"path/filepath"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestLocalConfigEnablesKernelManagedStorageBootstrap(t *testing.T) {
	path := filepath.Join("..", "..", "configs", "config.local.yaml")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read local config: %v", err)
	}
	var cfg struct {
		Data struct {
			Database struct {
				Enabled bool `yaml:"enabled"`
				Config  struct {
					AutoCreateDatabase bool `yaml:"auto_create_database"`
				} `yaml:"config"`
			} `yaml:"database"`
			ObjectStore struct {
				Enabled bool `yaml:"enabled"`
				Config  struct {
					EnsureBucket bool `yaml:"ensure_bucket"`
				} `yaml:"config"`
			} `yaml:"object_store"`
		} `yaml:"data"`
	}
	if err := yaml.Unmarshal(body, &cfg); err != nil {
		t.Fatalf("unmarshal local config: %v", err)
	}

	if !cfg.Data.Database.Enabled {
		t.Fatal("local database must be enabled")
	}
	if !cfg.Data.Database.Config.AutoCreateDatabase {
		t.Fatal("local database must enable kernel dbx auto_create_database")
	}
	if !cfg.Data.ObjectStore.Enabled {
		t.Fatal("local object_store must be enabled")
	}
	if !cfg.Data.ObjectStore.Config.EnsureBucket {
		t.Fatal("local object_store must enable kernel objectstorex ensure_bucket")
	}
}
