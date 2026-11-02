package config

import (
	"io"
	"net/url"
	"os"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

type deploymentConfigManifest struct {
	Kind       string            `yaml:"kind"`
	Data       map[string]string `yaml:"data"`
	StringData map[string]string `yaml:"stringData"`
}

func TestProductionDeploymentEnvironmentLoadsProfile(t *testing.T) {
	configMap := deploymentManifest(t, "../../deployments/kubernetes.yaml", "ConfigMap")
	secret := deploymentManifest(t, "../../deployments/secret.example.yaml", "Secret")
	for key, value := range configMap.Data {
		if strings.HasPrefix(key, "APP_") {
			t.Setenv(key, value)
		}
	}
	for key, value := range secret.StringData {
		if strings.HasPrefix(key, "APP_") {
			t.Setenv(key, value)
		}
	}

	cfg, err := LoadWithProfile("../../config/config.yaml", "production")
	if err != nil {
		t.Fatalf("load production deployment environment: %v", err)
	}
	if cfg.Database.Name != "go_api_template_db" || cfg.Database.Schema != "go_api_template" || cfg.Database.DSN == "" || !cfg.Migration.AutoUp || cfg.Migration.DatabaseURL == "" || cfg.Migration.DatabaseName != cfg.Database.Name || cfg.Migration.Schema != cfg.Database.Schema {
		t.Fatalf("production database and migration configuration differ: database_name=%q database_schema=%q has_database_dsn=%t migration_enabled=%t has_migration_url=%t migration_name=%q migration_schema=%q",
			cfg.Database.Name, cfg.Database.Schema, cfg.Database.DSN != "", cfg.Migration.AutoUp, cfg.Migration.DatabaseURL != "", cfg.Migration.DatabaseName, cfg.Migration.Schema)
	}
	for label, raw := range map[string]string{"application": cfg.Database.DSN, "migration": cfg.Migration.DatabaseURL} {
		parsed, err := url.Parse(raw)
		if err != nil || strings.TrimPrefix(parsed.Path, "/") != cfg.Database.Name {
			t.Fatalf("%s deployment URL does not select configured database %q", label, cfg.Database.Name)
		}
	}
}

func deploymentManifest(t *testing.T, path, kind string) deploymentConfigManifest {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	decoder := yaml.NewDecoder(file)
	for {
		var manifest deploymentConfigManifest
		if err := decoder.Decode(&manifest); err != nil {
			if err == io.EOF {
				t.Fatalf("%s has no %s", path, kind)
			}
			t.Fatal(err)
		}
		if manifest.Kind == kind {
			return manifest
		}
	}
}
