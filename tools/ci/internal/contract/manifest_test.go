package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveManifest(t *testing.T) {
	manifest := `dist:
  version: 2.1.0
receivers:
  - gomod: go.opentelemetry.io/collector/receiver/otlpreceiver v0.158.0
exporters:
  - gomod: example/exporter v0.158.0
`

	resolution, err := ResolveManifest(strings.NewReader(manifest))
	if err != nil {
		t.Fatalf("ResolveManifest() error = %v", err)
	}
	if resolution.Version != "2.1.0" || resolution.UpstreamPin != "v0.158.0" || resolution.UpstreamVersion != "0.158.0" {
		t.Fatalf("ResolveManifest() = %#v", resolution)
	}
}

func TestResolveManifestRejectsInvalidContracts(t *testing.T) {
	tests := []struct {
		name     string
		manifest string
	}{
		{name: "missing version", manifest: "receivers:\n  - gomod: go.opentelemetry.io/collector/receiver/otlpreceiver v0.158.0\n"},
		{name: "quoted version", manifest: "dist:\n  version: \"2.1.0\"\nreceivers:\n  - gomod: go.opentelemetry.io/collector/receiver/otlpreceiver v0.158.0\n"},
		{name: "missing receiver", manifest: "dist:\n  version: 2.1.0\n"},
		{name: "invalid upstream", manifest: "dist:\n  version: 2.1.0\nreceivers:\n  - gomod: go.opentelemetry.io/collector/receiver/otlpreceiver latest\n"},
		{name: "mixed pins", manifest: "dist:\n  version: 2.1.0\nreceivers:\n  - gomod: go.opentelemetry.io/collector/receiver/otlpreceiver v0.158.0\nexporters:\n  - gomod: example/exporter v0.157.0\n"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ResolveManifest(strings.NewReader(test.manifest)); err == nil {
				t.Fatal("ResolveManifest() error = nil, want an error")
			}
		})
	}
}

func TestCheckAdaptationVersions(t *testing.T) {
	directory := t.TempDir()
	matching := filepath.Join(directory, "matching")
	different := filepath.Join(directory, "different")
	if err := os.WriteFile(matching, []byte("v0.158.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(different, []byte("v0.157.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := CheckAdaptationVersions("v0.158.0", []string{matching}); err != nil {
		t.Fatalf("CheckAdaptationVersions() error = %v", err)
	}
	if err := CheckAdaptationVersions("v0.158.0", []string{different}); err == nil {
		t.Fatal("CheckAdaptationVersions() mismatch error = nil")
	}
	if err := CheckAdaptationVersions("v0.158.0", []string{filepath.Join(directory, "absent")}); err == nil {
		t.Fatal("CheckAdaptationVersions() missing file error = nil")
	}
}

func TestCheckAdaptationModules(t *testing.T) {
	directory := t.TempDir()
	matching := filepath.Join(directory, "matching.mod")
	mixedUpstream := filepath.Join(directory, "mixed-upstream.mod")
	mixedStable := filepath.Join(directory, "mixed-stable.mod")
	noCollector := filepath.Join(directory, "no-collector.mod")
	files := map[string]string{
		matching: `module example
require (
  go.opentelemetry.io/collector/exporter/otlpexporter v0.158.0
  go.opentelemetry.io/collector/component v1.64.0
)
`,
		mixedUpstream: "module example\nrequire go.opentelemetry.io/collector/exporter/otlpexporter v0.157.0\n",
		mixedStable: `module example
require (
  go.opentelemetry.io/collector/exporter/otlpexporter v0.158.0
  go.opentelemetry.io/collector/component v1.64.0
  go.opentelemetry.io/collector/consumer v1.63.0
)
`,
		noCollector: "module example\nrequire github.com/stretchr/testify v1.11.1\n",
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	stablePin, err := CheckAdaptationModules("v0.158.0", []string{matching})
	requireNoError(t, err)
	if stablePin != "v1.64.0" {
		t.Fatalf("CheckAdaptationModules() stable pin = %q", stablePin)
	}

	for _, path := range []string{mixedUpstream, mixedStable, noCollector, filepath.Join(directory, "absent.mod")} {
		if _, err := CheckAdaptationModules("v0.158.0", []string{path}); err == nil {
			t.Fatalf("CheckAdaptationModules(%q) error = nil", path)
		}
	}
}

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
