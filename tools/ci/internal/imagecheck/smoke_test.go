package imagecheck

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestVerifyRootFS(t *testing.T) {
	if err := verifyRootFS(bytes.NewReader(rootFSArchive(t, nil))); err != nil {
		t.Fatalf("verifyRootFS() error = %v", err)
	}
}

func TestVerifyRootFSRejectsInvalidEntries(t *testing.T) {
	tests := []struct {
		name   string
		change func([]tar.Header) []tar.Header
	}{
		{
			name: "missing",
			change: func(headers []tar.Header) []tar.Header {
				return headers[1:]
			},
		},
		{
			name: "duplicate",
			change: func(headers []tar.Header) []tar.Header {
				return append(headers, headers[0])
			},
		},
		{
			name: "wrong owner",
			change: func(headers []tar.Header) []tar.Header {
				headers[0].Uid = 10001
				return headers
			},
		},
		{
			name: "wrong mode",
			change: func(headers []tar.Header) []tar.Header {
				headers[0].Mode = 0o644
				return headers
			},
		},
		{
			name: "wrong type",
			change: func(headers []tar.Header) []tar.Header {
				headers[0].Typeflag = tar.TypeReg
				return headers
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := verifyRootFS(bytes.NewReader(rootFSArchive(t, test.change))); err == nil {
				t.Fatal("verifyRootFS() error = nil, want an error")
			}
		})
	}
}

func TestWaitForReadinessRequiresRunningBeforeAndAfterProbe(t *testing.T) {
	tests := []struct {
		name   string
		states []string
	}{
		{name: "stopped before probe", states: []string{"false"}},
		{name: "stopped after probe", states: []string{"true", "false"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &readinessRunner{states: test.states}
			if err := waitForReadiness(context.Background(), runner, "runtime", "probe", "probe-image", time.Second); err == nil {
				t.Fatal("waitForReadiness() error = nil, want an error")
			}
		})
	}
}

func TestWaitForReadinessProbesIsolatedNamespace(t *testing.T) {
	runner := &readinessRunner{states: []string{"true", "true"}}
	if err := waitForReadiness(context.Background(), runner, "runtime", "probe", "probe-image", time.Second); err != nil {
		t.Fatalf("waitForReadiness() error = %v", err)
	}

	want := []string{
		"docker", "run", "--rm",
		"--name", "probe",
		"--network", "container:runtime",
		"--read-only",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--entrypoint", "/bin/busybox",
		"probe-image",
		"wget", "--quiet", "--output-document=-", readinessURL,
	}
	if !slices.Equal(runner.probeCommand, want) {
		t.Fatalf("probe command = %q, want %q", runner.probeCommand, want)
	}
}

func TestWaitForReadinessHonoursContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runner := &readinessRunner{
		states:   []string{"true"},
		probeErr: errors.New("probe interrupted"),
	}
	err := waitForReadiness(ctx, runner, "runtime", "probe", "probe-image", time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("waitForReadiness() error = %v, want context.Canceled", err)
	}
}

func TestContainerIsRunningRejectsUnknownState(t *testing.T) {
	runner := commandRunnerFunc(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("unknown\n"), nil
	})
	if _, err := containerIsRunning(context.Background(), runner, "runtime"); err == nil {
		t.Fatal("containerIsRunning() error = nil, want an error")
	}
}

func TestRunDockerIncludesCommandOutput(t *testing.T) {
	runner := commandRunnerFunc(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("daemon failure\n"), errors.New("exit status 1")
	})
	_, err := runDocker(context.Background(), runner, "inspect", "image")
	var dockerErr *commandError
	if !errors.As(err, &dockerErr) {
		t.Fatalf("runDocker() error = %v, want *commandError", err)
	}
	if string(dockerErr.output) != "daemon failure\n" {
		t.Fatalf("command output = %q, want daemon failure", dockerErr.output)
	}
}

type commandRunnerFunc func(context.Context, string, ...string) ([]byte, error)

func (run commandRunnerFunc) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return run(ctx, name, args...)
}

type readinessRunner struct {
	states       []string
	probeCommand []string
	probeOutput  []byte
	probeErr     error
}

func (runner *readinessRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	command := append([]string{name}, args...)
	if len(args) > 0 && args[0] == "inspect" {
		if len(runner.states) == 0 {
			return nil, errors.New("unexpected inspect")
		}
		state := runner.states[0]
		runner.states = runner.states[1:]
		return []byte(state), nil
	}
	if len(args) > 0 && args[0] == "run" {
		runner.probeCommand = command
		if runner.probeOutput == nil {
			runner.probeOutput = []byte(`{"status":"StatusOK"}`)
		}
		return runner.probeOutput, runner.probeErr
	}
	return nil, fmt.Errorf("unexpected command %q", command)
}

func rootFSArchive(t *testing.T, change func([]tar.Header) []tar.Header) []byte {
	t.Helper()
	headers := make([]tar.Header, 0, len(requiredRootFSEntries))
	for _, entry := range requiredRootFSEntries {
		headers = append(headers, tar.Header{
			Name:     entry.path,
			Typeflag: entry.typeflag,
			Mode:     entry.mode,
			Uid:      0,
			Gid:      0,
			Size:     0,
		})
	}
	if change != nil {
		headers = change(headers)
	}

	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	for index := range headers {
		if err := writer.WriteHeader(&headers[index]); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

func TestSmokeRunsTheCompleteImageContract(t *testing.T) {
	config := writeSmokeConfig(t)
	runner := &smokeRunner{
		rootFS:        rootFSArchive(t, nil),
		states:        []string{"true", "true"},
		strandedProbe: true,
	}
	var output bytes.Buffer

	err := smoke(context.Background(), Options{
		Image:      "registry/image@sha256:digest",
		ConfigPath: config,
		ProbeImage: "probe-image@sha256:digest",
		Timeout:    time.Second,
		Pull:       true,
	}, &output, runner)
	if err != nil {
		t.Fatalf("smoke() error = %v", err)
	}
	if !strings.Contains(output.String(), "image runtime smoke passed") {
		t.Fatalf("smoke() output = %q", output.String())
	}

	runtimeCommand := runner.commandStartingWith("docker", "run", "--detach")
	wantAdjacentArguments := []string{
		"--network", "none",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--config", configTarget,
	}
	for index := 0; index < len(wantAdjacentArguments); index += 2 {
		if !containsAdjacent(runtimeCommand, wantAdjacentArguments[index], wantAdjacentArguments[index+1]) {
			t.Fatalf("runtime command %q lacks %q", runtimeCommand, wantAdjacentArguments[index:index+2])
		}
	}
	for _, argument := range []string{"--read-only", "registry/image@sha256:digest"} {
		if !slices.Contains(runtimeCommand, argument) {
			t.Fatalf("runtime command %q lacks %q", runtimeCommand, argument)
		}
	}
	if slices.Contains(runtimeCommand, "--user") {
		t.Fatalf("runtime command overrides the image user: %q", runtimeCommand)
	}
	wantMount := fmt.Sprintf("type=bind,source=%s,target=%s,readonly", config, configTarget)
	if !containsAdjacent(runtimeCommand, "--mount", wantMount) {
		t.Fatalf("runtime command %q does not mount the smoke config at %s", runtimeCommand, configTarget)
	}
	if !runner.calledWithPrefix("docker", "export", "--output") {
		t.Fatalf("commands %q do not export the rootfs", runner.commands)
	}
	if !runner.calledWithPrefix("docker", "pull", "registry/image@sha256:digest") {
		t.Fatalf("commands %q do not pull the digest", runner.commands)
	}
	if count := runner.countCommands("docker", "rm", "--force"); count != 3 {
		t.Fatalf("cleanup command count = %d, want 3; commands: %q", count, runner.commands)
	}
	if _, err := os.Stat(runner.rootFSPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary rootfs still exists at %q: %v", runner.rootFSPath, err)
	}
}

func TestRemoveContainerIfPresent(t *testing.T) {
	tests := []struct {
		name         string
		probeExists  bool
		wantRemovals int
	}{
		{name: "absent"},
		{name: "present", probeExists: true, wantRemovals: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &smokeRunner{existingContainers: map[string]bool{"probe": test.probeExists}}
			if err := removeContainerIfPresent(context.Background(), runner, "probe"); err != nil {
				t.Fatalf("removeContainerIfPresent() error = %v", err)
			}
			if count := runner.countCommands("docker", "rm", "--force"); count != test.wantRemovals {
				t.Fatalf("remove command count = %d, want %d", count, test.wantRemovals)
			}
		})
	}
}

func TestSmokeRemovesRuntimeContainerWhenDockerRunFails(t *testing.T) {
	runErr := errors.New("docker run failed after creating the container")
	runner := &smokeRunner{
		rootFS:        rootFSArchive(t, nil),
		runtimeRunErr: runErr,
		logs:          "collector failed during startup",
	}

	err := smoke(context.Background(), Options{
		Image:      "image",
		ConfigPath: writeSmokeConfig(t),
		ProbeImage: "probe-image",
		Timeout:    time.Second,
	}, io.Discard, runner)
	if !errors.Is(err, runErr) {
		t.Fatalf("smoke() error = %v, want docker run failure", err)
	}
	if runner.runtimeContainer == "" {
		t.Fatal("runtime container name is empty")
	}
	if runner.existingContainers[runner.runtimeContainer] {
		t.Fatalf("runtime container %q was not removed", runner.runtimeContainer)
	}
	if !runner.calledWithPrefix("docker", "rm", "--force", runner.runtimeContainer) {
		t.Fatalf("commands %q do not remove the failed runtime container", runner.commands)
	}
	if runner.existingContainers[runner.layoutContainer] {
		t.Fatalf("layout container %q was not removed", runner.layoutContainer)
	}
	for _, suffix := range []string{"-probe", "-runtime", "-layout"} {
		if !runner.checkedContainerWithSuffix(suffix) {
			t.Fatalf("cleanup did not check a container ending in %q: %q", suffix, runner.commands)
		}
	}
}

func TestSmokeRejectsWrongImageUser(t *testing.T) {
	runner := &smokeRunner{imageUser: "0:0"}
	err := smoke(context.Background(), Options{
		Image:      "image",
		ConfigPath: writeSmokeConfig(t),
		Timeout:    time.Second,
	}, io.Discard, runner)
	if err == nil {
		t.Fatal("smoke() error = nil, want an error")
	}
}

func TestSmokeReportsRuntimeLogsOnFailure(t *testing.T) {
	runner := &smokeRunner{
		rootFS: rootFSArchive(t, nil),
		states: []string{"false"},
		logs:   "collector startup failure",
	}
	err := smoke(context.Background(), Options{
		Image:      "image",
		ConfigPath: writeSmokeConfig(t),
		ProbeImage: "probe-image",
		Timeout:    time.Second,
	}, io.Discard, runner)
	var failure *runtimeError
	if !errors.As(err, &failure) {
		t.Fatalf("smoke() error = %v, want *runtimeError", err)
	}
	if string(failure.logs) != runner.logs {
		t.Fatalf("runtime logs = %q, want %q", failure.logs, runner.logs)
	}
}

func TestSmokeRejectsMissingConfig(t *testing.T) {
	err := Smoke(context.Background(), Options{
		Image:      "image",
		ConfigPath: filepath.Join(t.TempDir(), "missing.yaml"),
		Timeout:    time.Second,
	}, io.Discard)
	if err == nil {
		t.Fatal("Smoke() error = nil, want an error")
	}
}

func TestSmokeRejectsInvalidOptions(t *testing.T) {
	config := writeSmokeConfig(t)

	tests := []Options{
		{ConfigPath: config, Timeout: time.Second},
		{Image: "image", Timeout: time.Second},
		{Image: "image", ConfigPath: config},
	}
	for _, options := range tests {
		if err := Smoke(context.Background(), options, io.Discard); err == nil {
			t.Fatalf("Smoke(%+v) error = nil, want an error", options)
		}
	}
}

type smokeRunner struct {
	rootFS             []byte
	rootFSPath         string
	states             []string
	logs               string
	imageUser          string
	strandedProbe      bool
	runtimeRunErr      error
	runtimeContainer   string
	layoutContainer    string
	existingContainers map[string]bool
	checkedContainers  map[string]int
	commands           [][]string
}

func (runner *smokeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	command := append([]string{name}, args...)
	runner.commands = append(runner.commands, command)
	if runner.existingContainers == nil {
		runner.existingContainers = make(map[string]bool)
	}
	if runner.checkedContainers == nil {
		runner.checkedContainers = make(map[string]int)
	}

	switch {
	case len(args) > 0 && args[0] == "pull":
		return []byte("pulled\n"), nil
	case slices.Equal(command[:min(len(command), 5)], []string{"docker", "image", "inspect", "--format", "{{.Config.User}}"}):
		if runner.imageUser == "" {
			runner.imageUser = "10001:10001"
		}
		return []byte(runner.imageUser + "\n"), nil
	case len(args) > 0 && args[0] == "create":
		runner.layoutContainer = args[2]
		runner.existingContainers[runner.layoutContainer] = true
		return []byte("layout-id\n"), nil
	case len(args) > 2 && args[0] == "export" && args[1] == "--output":
		runner.rootFSPath = args[2]
		if err := os.WriteFile(args[2], runner.rootFS, 0o600); err != nil {
			return nil, err
		}
		return nil, nil
	case len(args) > 1 && args[0] == "run" && args[1] == "--detach":
		runner.runtimeContainer = args[3]
		runner.existingContainers[runner.runtimeContainer] = true
		return []byte("runtime-id\n"), runner.runtimeRunErr
	case len(args) > 1 && args[0] == "run" && args[1] == "--rm":
		if runner.strandedProbe {
			runner.existingContainers[args[3]] = true
		}
		return []byte(`{"status":"StatusOK"}`), nil
	case len(args) > 0 && args[0] == "inspect":
		if len(runner.states) == 0 {
			return nil, errors.New("unexpected inspect")
		}
		state := runner.states[0]
		runner.states = runner.states[1:]
		return []byte(state), nil
	case len(args) > 0 && args[0] == "logs":
		return []byte(runner.logs), nil
	case len(args) > 0 && args[0] == "ps":
		filter := args[len(args)-1]
		container := strings.TrimSuffix(strings.TrimPrefix(filter, "name=^/"), "$")
		runner.checkedContainers[container]++
		if runner.existingContainers[container] {
			return []byte("probe-id\n"), nil
		}
		return nil, nil
	case len(args) > 0 && args[0] == "rm":
		delete(runner.existingContainers, args[2])
		return nil, nil
	default:
		return nil, fmt.Errorf("unexpected command %q", command)
	}
}

func (runner *smokeRunner) commandStartingWith(prefix ...string) []string {
	for _, command := range runner.commands {
		if len(command) >= len(prefix) && slices.Equal(command[:len(prefix)], prefix) {
			return command
		}
	}
	return nil
}

func (runner *smokeRunner) calledWithPrefix(prefix ...string) bool {
	return runner.commandStartingWith(prefix...) != nil
}

func (runner *smokeRunner) countCommands(prefix ...string) int {
	count := 0
	for _, command := range runner.commands {
		if len(command) >= len(prefix) && slices.Equal(command[:len(prefix)], prefix) {
			count++
		}
	}
	return count
}

func (runner *smokeRunner) checkedContainerWithSuffix(suffix string) bool {
	for container := range runner.checkedContainers {
		if strings.HasSuffix(container, suffix) {
			return true
		}
	}
	return false
}

func containsAdjacent(values []string, first, second string) bool {
	for index := 0; index+1 < len(values); index++ {
		if values[index] == first && values[index+1] == second {
			return true
		}
	}
	return false
}

func writeSmokeConfig(t *testing.T) string {
	t.Helper()
	config := filepath.Join(t.TempDir(), "image-smoke.yaml")
	if err := os.WriteFile(config, []byte("service: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return config
}
