package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestStagingPromotionChecksLiveDeploymentDigestAndProfile(t *testing.T) {
	const image = "ghcr.io/lihongjie0209/go-api-template@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	stubDir := t.TempDir()
	kubectl := `#!/bin/sh
case "$*" in
  *"rollout status"*) [ "$STUB_ROLLOUT_FAIL" != 1 ] ;;
  *"get deployment"*) printf '%s' "$STUB_IMAGE" ;;
  *"get configmap"*) printf '%s' "$STUB_PROFILE" ;;
  *"get namespace"*) printf '%s' "$STUB_NAMESPACE_ENV" ;;
  *) exit 3 ;;
esac
`
	if err := os.WriteFile(filepath.Join(stubDir, "kubectl"), []byte(kubectl), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name        string
		image       string
		profile     string
		namespace   string
		rolloutFail bool
		wantError   string
	}{
		{name: "verified", image: image, profile: "staging", namespace: "staging"},
		{name: "different digest", image: strings.Replace(image, strings.Repeat("a", 64), strings.Repeat("b", 64), 1), profile: "staging", namespace: "staging", wantError: "requested image digest"},
		{name: "wrong profile", image: image, profile: "production", namespace: "staging", wantError: "staging profile"},
		{name: "wrong namespace label", image: image, profile: "staging", namespace: "production", wantError: "environment label"},
		{name: "rollout incomplete", image: image, profile: "staging", namespace: "staging", rolloutFail: true, wantError: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := exec.Command("sh", "../../scripts/verify-staging-image.sh", image)
			failure := "0"
			if test.rolloutFail {
				failure = "1"
			}
			command.Env = append(os.Environ(),
				"PATH="+stubDir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"STUB_IMAGE="+test.image,
				"STUB_PROFILE="+test.profile,
				"STUB_NAMESPACE_ENV="+test.namespace,
				"STUB_ROLLOUT_FAIL="+failure,
			)
			output, err := command.CombinedOutput()
			if test.name == "verified" {
				if err != nil {
					t.Fatalf("verified staging rejected: %v: %s", err, output)
				}
				return
			}
			if err == nil || test.wantError != "" && !strings.Contains(string(output), test.wantError) {
				t.Fatalf("staging %s result = (%v, %q), want failure containing %q", test.name, err, output, test.wantError)
			}
		})
	}
}
