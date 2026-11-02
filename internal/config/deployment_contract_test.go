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
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name   string            `yaml:"name"`
		Labels map[string]string `yaml:"labels"`
	} `yaml:"metadata"`
	Data       map[string]string `yaml:"data"`
	StringData map[string]string `yaml:"stringData"`
	Spec       struct {
		Ingress []struct {
			From []struct {
				PodSelector       *map[string]string `yaml:"podSelector"`
				NamespaceSelector struct {
					MatchLabels map[string]string `yaml:"matchLabels"`
				} `yaml:"namespaceSelector"`
			} `yaml:"from"`
			Ports []struct {
				Port int `yaml:"port"`
			} `yaml:"ports"`
		} `yaml:"ingress"`
		Template struct {
			Spec struct {
				Containers []struct {
					EnvFrom []struct {
						ConfigMapRef struct {
							Name string `yaml:"name"`
						} `yaml:"configMapRef"`
					} `yaml:"envFrom"`
					Env []struct {
						Name      string `yaml:"name"`
						ValueFrom struct {
							SecretKeyRef struct {
								Name string `yaml:"name"`
								Key  string `yaml:"key"`
							} `yaml:"secretKeyRef"`
						} `yaml:"valueFrom"`
					} `yaml:"env"`
				} `yaml:"containers"`
			} `yaml:"spec"`
		} `yaml:"template"`
	} `yaml:"spec"`
}

func TestProductionNetworkPolicyAllowsOnlySameEnvironmentNamespaces(t *testing.T) {
	namespace := deploymentManifest(t, "../../deployments/kubernetes.yaml", "Namespace")
	policy := deploymentManifest(t, "../../deployments/kubernetes.yaml", "NetworkPolicy")
	environment := namespace.Metadata.Labels["platform-environment"]
	if environment != "production" {
		t.Fatalf("production Namespace environment label = %q", environment)
	}
	if len(policy.Spec.Ingress) != 1 || len(policy.Spec.Ingress[0].From) != 2 {
		t.Fatalf("production NetworkPolicy ingress peers = %+v", policy.Spec.Ingress)
	}
	if own := policy.Spec.Ingress[0].From[0].PodSelector; own == nil || len(*own) != 0 {
		t.Fatalf("production NetworkPolicy does not allow its own namespace")
	}
	peer := policy.Spec.Ingress[0].From[1].NamespaceSelector.MatchLabels
	if len(peer) != 1 || peer["platform-environment"] != environment {
		t.Fatalf("production NetworkPolicy namespace selector = %v, want platform-environment=%q", peer, environment)
	}
	ports := policy.Spec.Ingress[0].Ports
	if len(ports) != 2 || ports[0].Port != 8080 || ports[1].Port != 9090 {
		t.Fatalf("production NetworkPolicy ingress ports = %v, want HTTP 8080 and gRPC 9090", ports)
	}
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

func TestProductionMigrationJobUsesApplicationConfigMap(t *testing.T) {
	configMap := deploymentManifest(t, "../../deployments/kubernetes.yaml", "ConfigMap")
	secret := deploymentManifest(t, "../../deployments/secret.example.yaml", "Secret")
	job := deploymentManifest(t, "../../deployments/migrate-job.yaml", "Job")
	if len(job.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("migration Job has %d containers, want 1", len(job.Spec.Template.Spec.Containers))
	}
	container := job.Spec.Template.Spec.Containers[0]
	configMapFound := false
	for _, source := range container.EnvFrom {
		configMapFound = configMapFound || source.ConfigMapRef.Name == configMap.Metadata.Name
	}
	if !configMapFound {
		t.Fatalf("migration Job does not load application ConfigMap %q", configMap.Metadata.Name)
	}
	urlFound := false
	for _, entry := range container.Env {
		if entry.Name == "APP_MIGRATION_DATABASE_URL" && entry.ValueFrom.SecretKeyRef.Name == secret.Metadata.Name && entry.ValueFrom.SecretKeyRef.Key == entry.Name {
			urlFound = true
		}
	}
	if !urlFound {
		t.Fatal("migration Job does not load its URL from the application Secret")
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
