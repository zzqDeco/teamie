package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunnerImageAcceptsImmutablePlatformReferences(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	for name, ref := range map[string]string{
		"local image ID":       digest,
		"legacy GHCR":          "ghcr.io/zzqdeco/teamie-sandbox-runner@" + digest,
		"legacy ACR public":    "registry.cn-shenzhen.aliyuncs.com/teamie/teamie-sandbox-runner@" + digest,
		"legacy ACR VPC":       "registry-vpc.cn-shenzhen.aliyuncs.com/teamie/teamie-sandbox-runner@" + digest,
		"personal ACR public":  "crpi-abc123.cn-shenzhen.personal.cr.aliyuncs.com/teamie/teamie-sandbox-runner@" + digest,
		"personal ACR VPC":     "crpi-abc123-vpc.cn-shenzhen.personal.cr.aliyuncs.com/teamie/teamie-sandbox-runner@" + digest,
		"enterprise public":    "teamie-prod-registry.cn-shenzhen.cr.aliyuncs.com/teamie/teamie-sandbox-runner@" + digest,
		"enterprise VPC":       "teamie-prod-registry-vpc.cn-shenzhen.cr.aliyuncs.com/teamie/teamie-sandbox-runner@" + digest,
		"other ACR region":     "registry.ap-southeast-1.aliyuncs.com/teamie/teamie-sandbox-runner@" + digest,
		"safe namespace":       "registry.cn-hangzhou.aliyuncs.com/teamie_ci.v3-prod/teamie-sandbox-runner@" + digest,
		"namespace maximum":    "registry.cn-hangzhou.aliyuncs.com/" + strings.Repeat("a", 63) + "/teamie-sandbox-runner@" + digest,
		"hostname DNS maximum": strings.Repeat("a", 50) + "-registry-vpc.cn-shenzhen.cr.aliyuncs.com/teamie/teamie-sandbox-runner@" + digest,
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			s, err := New(&fakeEngine{}, token, ref, filepath.Join(root, "workspaces"), filepath.Join(root, "state"))
			if err != nil {
				t.Fatalf("rejected supported runner reference: %v", err)
			}
			t.Cleanup(s.Close)
			if s.Image != ref {
				t.Fatalf("runner reference changed: got %q, want %q", s.Image, ref)
			}
		})
	}
}

func TestRunnerImageRejectsMutableOrUntrustedReferencesBeforeInitialization(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	host := "registry.cn-shenzhen.aliyuncs.com"
	repo := "/teamie/teamie-sandbox-runner"
	ref := host + repo + "@" + digest
	for name, image := range map[string]string{
		"empty":                       "",
		"tag":                         host + repo + ":latest",
		"tag and digest":              host + repo + ":latest@" + digest,
		"other repository":            host + "/teamie/teamie-agent-runtime@" + digest,
		"repository prefix":           host + "/teamie/teamie-sandbox-runner-extra@" + digest,
		"other GHCR owner":            "ghcr.io/another/teamie-sandbox-runner@" + digest,
		"GHCR lookalike":              "ghcrXio/zzqdeco/teamie-sandbox-runner@" + digest,
		"other registry":              "docker.io" + repo + "@" + digest,
		"ACR suffix spoof":            host + ".attacker.example" + repo + "@" + digest,
		"ACR hostname prefix":         "attacker." + host + repo + "@" + digest,
		"arbitrary Aliyun service":    "evil.cn-shenzhen.aliyuncs.com" + repo + "@" + digest,
		"legacy hostname lookalike":   "registry-evil.cn-shenzhen.aliyuncs.com" + repo + "@" + digest,
		"personal hostname lookalike": "crpi-abc123.cn-shenzhen.cr.aliyuncs.com" + repo + "@" + digest,
		"missing personal instance":   "crpi-.cn-shenzhen.personal.cr.aliyuncs.com" + repo + "@" + digest,
		"incorrect enterprise domain": "teamie.cr.cn-shenzhen.aliyuncs.com" + repo + "@" + digest,
		"invalid enterprise instance": "teamie_prod-registry.cn-shenzhen.cr.aliyuncs.com" + repo + "@" + digest,
		"instance leading hyphen":     "-teamie-registry.cn-shenzhen.cr.aliyuncs.com" + repo + "@" + digest,
		"empty enterprise instance":   "-registry.cn-shenzhen.cr.aliyuncs.com" + repo + "@" + digest,
		"invalid region":              "registry.shenzhen.aliyuncs.com" + repo + "@" + digest,
		"region traversal":            "registry.cn-shenzhen.evil.aliyuncs.com" + repo + "@" + digest,
		"namespace traversal":         host + "/../teamie-sandbox-runner@" + digest,
		"nested namespace":            host + "/teamie/nested/teamie-sandbox-runner@" + digest,
		"encoded namespace slash":     host + "/teamie%2fnested/teamie-sandbox-runner@" + digest,
		"empty namespace":             host + "//teamie-sandbox-runner@" + digest,
		"namespace leading dot":       host + "/.teamie/teamie-sandbox-runner@" + digest,
		"namespace trailing hyphen":   host + "/teamie-/teamie-sandbox-runner@" + digest,
		"namespace uppercase":         host + "/Teamie/teamie-sandbox-runner@" + digest,
		"namespace repeated dot":      host + "/teamie..prod/teamie-sandbox-runner@" + digest,
		"namespace over limit":        host + "/" + strings.Repeat("a", 64) + "/teamie-sandbox-runner@" + digest,
		"hostname label over limit":   strings.Repeat("a", 51) + "-registry-vpc.cn-shenzhen.cr.aliyuncs.com" + repo + "@" + digest,
		"registry port":               host + ":443" + repo + "@" + digest,
		"registry credentials":        "user:password@" + ref,
		"registry URL":                "https://" + ref,
		"short digest":                ref[:len(ref)-1],
		"long digest":                 ref + "a",
		"uppercase digest":            host + repo + "@sha256:" + strings.Repeat("A", 64),
		"nonhex digest":               host + repo + "@sha256:" + strings.Repeat("g", 64),
		"other digest algorithm":      host + repo + "@sha512:" + strings.Repeat("a", 64),
		"trailing newline":            ref + "\n",
		"leading whitespace":          " " + ref,
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			workspace, state := filepath.Join(root, "workspaces"), filepath.Join(root, "state")
			s, err := New(&fakeEngine{}, token, image, workspace, state)
			if err == nil {
				s.Close()
				t.Fatal("accepted mutable or untrusted runner reference")
			}
			if !strings.Contains(err.Error(), "SANDBOX_RUNNER_IMAGE") {
				t.Fatalf("reference failed for another reason: %v", err)
			}
			for _, path := range []string{workspace, state} {
				if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
					t.Fatalf("invalid image initialized sandbox root %q: %v", path, statErr)
				}
			}
		})
	}
}
