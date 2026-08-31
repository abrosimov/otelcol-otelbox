package contract

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestCheckBinary(t *testing.T) {
	manifest, components := completeContracts()
	var output bytes.Buffer

	if err := CheckBinary(strings.NewReader(manifest), []byte(components), &output); err != nil {
		t.Fatalf("CheckBinary() error = %v", err)
	}
	if !strings.Contains(output.String(), "manifest modules and required component names are present") {
		t.Fatalf("CheckBinary() output = %q", output.String())
	}
}

func TestCheckBinaryRejectsDrift(t *testing.T) {
	manifest, components := completeContracts()
	tests := []struct {
		name       string
		manifest   string
		components string
	}{
		{name: "empty manifest", manifest: "dist:\n", components: components},
		{name: "invalid manifest module", manifest: "receivers:\n  - gomod: incomplete\n", components: components},
		{name: "empty components", manifest: manifest},
		{name: "missing component", manifest: manifest, components: strings.Replace(components, "    - name: file\n      module: example/file v1.0.0\n", "", 1)},
		{name: "wrong module", manifest: manifest, components: strings.Replace(components, "example/otlp v1.0.0", "example/otlp v1.0.1", 1)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := CheckBinary(strings.NewReader(test.manifest), []byte(test.components), &bytes.Buffer{}); err == nil {
				t.Fatal("CheckBinary() error = nil, want an error")
			}
		})
	}
}

func TestParsersRejectMalformedBinaryModule(t *testing.T) {
	_, err := parseComponents(strings.NewReader("receivers:\n    - name: otlp\n      module: incomplete\n"))
	if err == nil {
		t.Fatal("parseComponents() error = nil, want an error")
	}
}

func completeContracts() (string, string) {
	sections := map[string][]string{
		"receivers":  {"host_metrics", "journald", "otlp", "prometheus"},
		"processors": {"filter", "memory_limiter", "redaction", "resource", "resource_detection", "transform"},
		"exporters":  {"file", "otlp_grpc", "otlp_http"},
		"extensions": {"bearertokenauth", "file_storage", "headers_setter", "healthcheckv2"},
		"connectors": {},
	}

	var manifest strings.Builder
	var components strings.Builder
	for _, kind := range componentKinds {
		fmt.Fprintf(&manifest, "%s:\n", kind)
		fmt.Fprintf(&components, "%s:\n", kind)
		for _, name := range sections[kind] {
			module := "example/" + name
			if name == "otlp" {
				module = "example/otlp"
			}
			fmt.Fprintf(&manifest, "  - gomod: %s v1.0.0\n", module)
			fmt.Fprintf(&components, "    - name: %s\n      module: %s v1.0.0\n", name, module)
		}
	}
	return manifest.String(), components.String()
}
