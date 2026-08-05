package contract

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckSharedFiles(t *testing.T) {
	directory := t.TempDir()
	reference := writeProfile(t, directory, "reference.yaml", "same")
	matching := writeProfile(t, directory, "matching.yaml", "same")

	var output bytes.Buffer
	if err := CheckSharedFiles([]string{reference, matching}, &output); err != nil {
		t.Fatalf("CheckSharedFiles() error = %v", err)
	}
	if !strings.Contains(output.String(), "2 profiles agree on 3 shared regions") {
		t.Fatalf("CheckSharedFiles() output = %q", output.String())
	}
}

func TestCheckSharedFilesRejectsInvalidProfiles(t *testing.T) {
	directory := t.TempDir()
	reference := writeProfile(t, directory, "reference.yaml", "same")
	different := writeProfile(t, directory, "different.yaml", "different")
	missingMarker := filepath.Join(directory, "missing.yaml")
	if err := os.WriteFile(missingMarker, []byte("plain: yaml\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		paths []string
	}{
		{name: "one profile", paths: []string{reference}},
		{name: "missing file", paths: []string{reference, filepath.Join(directory, "absent.yaml")}},
		{name: "wrong marker count", paths: []string{reference, missingMarker}},
		{name: "different content", paths: []string{reference, different}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := CheckSharedFiles(test.paths, &bytes.Buffer{}); err == nil {
				t.Fatal("CheckSharedFiles() error = nil, want an error")
			}
		})
	}
}

func TestExtractSharedRejectsMarkerOrder(t *testing.T) {
	tests := []struct {
		name    string
		profile string
	}{
		{name: "nested", profile: openMarker + "\n" + openMarker + "\n"},
		{name: "closing first", profile: closeMarker + "\n"},
		{name: "unclosed", profile: openMarker + "\nvalue\n"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := extractShared(strings.NewReader(test.profile), "profile.yaml"); err == nil {
				t.Fatal("extractShared() error = nil, want an error")
			}
		})
	}
}

func writeProfile(t *testing.T, directory, name, value string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	content := strings.Repeat(openMarker+"\nvalue: "+value+"\n"+closeMarker+"\n", expectedBlocks)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
