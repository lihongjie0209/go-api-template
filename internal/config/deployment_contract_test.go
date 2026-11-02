package config

import (
	"bytes"
	"io"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

type deploymentConfigManifest struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name      string            `yaml:"name"`
		Namespace string            `yaml:"namespace"`
		Labels    map[string]string `yaml:"labels"`
	} `yaml:"metadata"`
	Data       map[string]string `yaml:"data"`
	StringData map[string]string `yaml:"stringData"`
	Immutable  bool              `yaml:"immutable"`
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
				SecurityContext struct {
					FSGroup int `yaml:"fsGroup"`
				} `yaml:"securityContext"`
				Volumes []struct {
					Name   string `yaml:"name"`
					Secret struct {
						SecretName  string `yaml:"secretName"`
						DefaultMode int    `yaml:"defaultMode"`
					} `yaml:"secret"`
				} `yaml:"volumes"`
				Containers []struct {
					Args         []string `yaml:"args"`
					VolumeMounts []struct {
						Name      string `yaml:"name"`
						MountPath string `yaml:"mountPath"`
						ReadOnly  bool   `yaml:"readOnly"`
					} `yaml:"volumeMounts"`
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

func TestStagingRenderKeepsEnvironmentAndNamespaceAligned(t *testing.T) {
	namespace := renderedDeploymentManifest(t, "application", "staging", "Namespace")
	configMap := renderedDeploymentManifest(t, "application", "staging", "ConfigMap")
	policy := renderedDeploymentManifest(t, "application", "staging", "NetworkPolicy")
	job := renderedDeploymentManifest(t, "migration", "staging", "Job")
	if namespace.Metadata.Name != "microservices-staging" || namespace.Metadata.Labels["platform-environment"] != "staging" {
		t.Fatalf("staging Namespace = %+v", namespace.Metadata)
	}
	if configMap.Metadata.Namespace != namespace.Metadata.Name || configMap.Data["APP_ENV"] != "staging" {
		t.Fatalf("staging ConfigMap namespace=%q APP_ENV=%q", configMap.Metadata.Namespace, configMap.Data["APP_ENV"])
	}
	if policy.Metadata.Namespace != namespace.Metadata.Name || len(policy.Spec.Ingress) != 1 || len(policy.Spec.Ingress[0].From) != 2 || policy.Spec.Ingress[0].From[1].NamespaceSelector.MatchLabels["platform-environment"] != "staging" {
		t.Fatalf("staging NetworkPolicy namespace=%q ingress=%+v", policy.Metadata.Namespace, policy.Spec.Ingress)
	}
	if job.Metadata.Namespace != namespace.Metadata.Name || len(job.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("staging migration Job namespace=%q containers=%d", job.Metadata.Namespace, len(job.Spec.Template.Spec.Containers))
	}
	args := job.Spec.Template.Spec.Containers[0].Args
	if !slices.Contains(args, "staging") || slices.Contains(args, "production") {
		t.Fatalf("staging migration arguments = %v", args)
	}
}

func renderedDeploymentManifest(t *testing.T, part, environment, kind string) deploymentConfigManifest {
	t.Helper()
	const image = "ghcr.io/lihongjie0209/go-api-template@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	command := exec.Command("sh", "../../scripts/render-deployment-manifest.sh", part, image, environment)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("render %s %s: %v: %s", environment, part, err, output)
	}
	return decodeDeploymentManifest(t, bytes.NewReader(output), kind, "")
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
	signingSecret := deploymentManifestNamed(t, "../../deployments/secret.example.yaml", "Secret", "go-api-template-signing-v1")
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
	for key, value := range signingSecret.StringData {
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

func TestProductionSigningKeyUsesReadOnlySecretVolume(t *testing.T) {
	configMap := deploymentManifest(t, "../../deployments/kubernetes.yaml", "ConfigMap")
	deployment := deploymentManifest(t, "../../deployments/kubernetes.yaml", "Deployment")
	applicationSecret := deploymentManifestNamed(t, "../../deployments/secret.example.yaml", "Secret", "go-api-template")
	signingSecret := deploymentManifestNamed(t, "../../deployments/secret.example.yaml", "Secret", "go-api-template-signing-v1")
	const signingPath = "/run/secrets/signing"
	if configMap.Data["APP_JWT_PRIVATE_KEY_FILE"] != signingPath+"/jwt-private.pem" {
		t.Fatalf("JWT private key file = %q", configMap.Data["APP_JWT_PRIVATE_KEY_FILE"])
	}
	if _, exists := applicationSecret.StringData["APP_JWT_PRIVATE_KEY"]; exists {
		t.Fatal("application environment Secret still contains JWT private key material")
	}
	if _, exists := applicationSecret.StringData["APP_JWT_KEY_ID"]; exists {
		t.Fatal("application environment Secret still owns the signing key ID")
	}
	if _, exists := signingSecret.StringData["jwt-private.pem"]; !exists {
		t.Fatal("signing Secret lacks jwt-private.pem")
	}
	if signingSecret.StringData["APP_JWT_KEY_ID"] == "" {
		t.Fatal("signing Secret lacks APP_JWT_KEY_ID")
	}
	if !signingSecret.Immutable {
		t.Fatal("versioned signing Secret must be immutable")
	}
	if len(deployment.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("deployment has %d containers", len(deployment.Spec.Template.Spec.Containers))
	}
	mounted := false
	container := deployment.Spec.Template.Spec.Containers[0]
	for _, mount := range container.VolumeMounts {
		mounted = mounted || mount.Name == "signing" && mount.MountPath == signingPath && mount.ReadOnly
	}
	keyIDFromSigningSecret := false
	for _, entry := range container.Env {
		keyIDFromSigningSecret = keyIDFromSigningSecret || entry.Name == "APP_JWT_KEY_ID" && entry.ValueFrom.SecretKeyRef.Name == signingSecret.Metadata.Name && entry.ValueFrom.SecretKeyRef.Key == entry.Name
	}
	volumeFound := false
	for _, volume := range deployment.Spec.Template.Spec.Volumes {
		volumeFound = volumeFound || volume.Name == "signing" && volume.Secret.SecretName == signingSecret.Metadata.Name && volume.Secret.DefaultMode == 0o440
	}
	if !mounted || !volumeFound || !keyIDFromSigningSecret || deployment.Spec.Template.Spec.SecurityContext.FSGroup == 0 {
		t.Fatalf("signing Secret mount invalid: mounted=%t volume_found=%t key_id_source=%t fs_group=%d", mounted, volumeFound, keyIDFromSigningSecret, deployment.Spec.Template.Spec.SecurityContext.FSGroup)
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
	return deploymentManifestNamed(t, path, kind, "")
}

func deploymentManifestNamed(t *testing.T, path, kind, name string) deploymentConfigManifest {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	return decodeDeploymentManifest(t, file, kind, name)
}

func decodeDeploymentManifest(t *testing.T, source io.Reader, kind, name string) deploymentConfigManifest {
	t.Helper()
	decoder := yaml.NewDecoder(source)
	for {
		var manifest deploymentConfigManifest
		if err := decoder.Decode(&manifest); err != nil {
			if err == io.EOF {
				t.Fatalf("manifest has no %s", kind)
			}
			t.Fatal(err)
		}
		if manifest.Kind == kind && (name == "" || manifest.Metadata.Name == name) {
			return manifest
		}
	}
}
