package imagecheck

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const (
	DefaultProbeImage = "alpine:3.24.1@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b"
	configTarget      = "/etc/otelbox/image-smoke.yaml"
	readinessURL      = "http://127.0.0.1:13133/status"
)

type Options struct {
	Image      string
	ConfigPath string
	ProbeImage string
	Timeout    time.Duration
	Pull       bool
}

type commandRunner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

type commandError struct {
	command []string
	output  []byte
	cause   error
}

func (err *commandError) Error() string {
	return fmt.Sprintf("%s: %v: %s", strings.Join(err.command, " "), err.cause, bytes.TrimSpace(err.output))
}

func (err *commandError) Unwrap() error {
	return err.cause
}

type runtimeError struct {
	cause error
	logs  []byte
}

func (err *runtimeError) Error() string {
	return fmt.Sprintf("%v\ncontainer logs:\n%s", err.cause, bytes.TrimSpace(err.logs))
}

func (err *runtimeError) Unwrap() error {
	return err.cause
}

type rootFSEntry struct {
	path     string
	typeflag byte
	mode     int64
}

var requiredRootFSEntries = []rootFSEntry{
	{path: "etc", typeflag: tar.TypeDir, mode: 0o755},
	{path: "etc/ssl", typeflag: tar.TypeDir, mode: 0o755},
	{path: "etc/ssl/certs", typeflag: tar.TypeDir, mode: 0o755},
	{path: "etc/ssl/certs/ca-certificates.crt", typeflag: tar.TypeReg, mode: 0o644},
	{path: "otelcol-otelbox", typeflag: tar.TypeReg, mode: 0o755},
}

func Smoke(ctx context.Context, options Options, stdout io.Writer) (err error) {
	return smoke(ctx, options, stdout, execRunner{})
}

func smoke(ctx context.Context, options Options, stdout io.Writer, runner commandRunner) (err error) {
	if options.Image == "" {
		return errors.New("image smoke requires --image")
	}
	if options.ConfigPath == "" {
		return errors.New("image smoke requires --config")
	}
	if options.ProbeImage == "" {
		options.ProbeImage = DefaultProbeImage
	}
	if options.Timeout <= 0 {
		return errors.New("image smoke requires a positive --timeout")
	}

	configPath, err := filepath.Abs(options.ConfigPath)
	if err != nil {
		return fmt.Errorf("resolve smoke config: %w", err)
	}
	if info, statErr := os.Stat(configPath); statErr != nil {
		return fmt.Errorf("stat smoke config: %w", statErr)
	} else if !info.Mode().IsRegular() {
		return fmt.Errorf("smoke config %s is not a regular file", configPath)
	}

	temporaryDir, err := os.MkdirTemp("", "otelbox-image-smoke-")
	if err != nil {
		return fmt.Errorf("create image smoke directory: %w", err)
	}
	containerPrefix := filepath.Base(temporaryDir)
	layoutContainer := containerPrefix + "-layout"
	runtimeContainer := containerPrefix + "-runtime"
	probeContainer := containerPrefix + "-probe"
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err != nil {
			runtimeExists, existsErr := containerExists(cleanupCtx, runner, runtimeContainer)
			if existsErr != nil {
				err = errors.Join(err, fmt.Errorf("find runtime container for logs: %w", existsErr))
			} else if runtimeExists {
				logs, logsErr := runDocker(cleanupCtx, runner, "logs", runtimeContainer)
				if logsErr == nil {
					err = &runtimeError{cause: err, logs: logs}
				} else {
					err = errors.Join(err, fmt.Errorf("read runtime logs: %w", logsErr))
				}
			}
		}
		for _, container := range []string{probeContainer, runtimeContainer, layoutContainer} {
			if removeErr := removeContainerIfPresent(cleanupCtx, runner, container); removeErr != nil {
				err = errors.Join(err, fmt.Errorf("remove container %s: %w", container, removeErr))
			}
		}
		if removeErr := os.RemoveAll(temporaryDir); removeErr != nil {
			err = errors.Join(err, fmt.Errorf("remove image smoke directory: %w", removeErr))
		}
	}()

	if options.Pull {
		if _, err = runDocker(ctx, runner, "pull", options.Image); err != nil {
			return err
		}
	}

	declaredUser, err := runDocker(ctx, runner, "image", "inspect", "--format", "{{.Config.User}}", options.Image)
	if err != nil {
		return err
	}
	if actual := strings.TrimSpace(string(declaredUser)); actual != "10001:10001" {
		return fmt.Errorf("image user is %q, want 10001:10001", actual)
	}

	if _, err = runDocker(ctx, runner, "create", "--name", layoutContainer, options.Image); err != nil {
		return err
	}
	rootFSPath := filepath.Join(temporaryDir, "rootfs.tar")
	if _, err = runDocker(ctx, runner, "export", "--output", rootFSPath, layoutContainer); err != nil {
		return err
	}
	rootFS, err := os.Open(rootFSPath)
	if err != nil {
		return fmt.Errorf("open exported rootfs: %w", err)
	}
	rootFSErr := verifyRootFS(rootFS)
	closeErr := rootFS.Close()
	if closeErr != nil {
		closeErr = fmt.Errorf("close exported rootfs: %w", closeErr)
	}
	if rootFSErr != nil || closeErr != nil {
		return errors.Join(rootFSErr, closeErr)
	}

	mount := fmt.Sprintf("type=bind,source=%s,target=%s,readonly", configPath, configTarget)
	if _, err = runDocker(ctx, runner,
		"run", "--detach",
		"--name", runtimeContainer,
		"--network", "none",
		"--read-only",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--mount", mount,
		options.Image,
		"--config", configTarget,
	); err != nil {
		return err
	}

	if err = waitForReadiness(ctx, runner, runtimeContainer, probeContainer, options.ProbeImage, options.Timeout); err != nil {
		return err
	}

	fmt.Fprintf(stdout, "image runtime smoke passed for %s\n", options.Image)
	return nil
}

func verifyRootFS(rootFS io.Reader) error {
	required := make(map[string]rootFSEntry, len(requiredRootFSEntries))
	for _, entry := range requiredRootFSEntries {
		required[entry.path] = entry
	}
	found := make(map[string]bool, len(requiredRootFSEntries))
	archive := tar.NewReader(rootFS)

	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read exported rootfs: %w", err)
		}
		entryPath := path.Clean(strings.TrimPrefix(header.Name, "./"))
		expected, ok := required[entryPath]
		if !ok {
			continue
		}
		if found[entryPath] {
			return fmt.Errorf("rootfs entry %q is duplicated", entryPath)
		}
		found[entryPath] = true
		if header.Uid != 0 || header.Gid != 0 {
			return fmt.Errorf("rootfs entry %q owner is %d:%d, want 0:0", entryPath, header.Uid, header.Gid)
		}
		if header.Typeflag != expected.typeflag && !(expected.typeflag == tar.TypeReg && header.Typeflag == tar.TypeRegA) {
			return fmt.Errorf("rootfs entry %q has tar type %d, want %d", entryPath, header.Typeflag, expected.typeflag)
		}
		if header.Mode != expected.mode {
			return fmt.Errorf("rootfs entry %q mode is %#o, want %#o", entryPath, header.Mode, expected.mode)
		}
	}

	for _, entry := range requiredRootFSEntries {
		if !found[entry.path] {
			return fmt.Errorf("rootfs entry %q is missing", entry.path)
		}
	}
	return nil
}

func waitForReadiness(
	ctx context.Context,
	runner commandRunner,
	runtimeContainer string,
	probeContainer string,
	probeImage string,
	timeout time.Duration,
) error {
	readinessCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	for {
		running, err := containerIsRunning(readinessCtx, runner, runtimeContainer)
		if err != nil {
			return err
		}
		if !running {
			return errors.New("container stopped before it became ready")
		}

		probeOutput, probeErr := runner.Run(readinessCtx,
			"docker", "run", "--rm",
			"--name", probeContainer,
			"--network", "container:"+runtimeContainer,
			"--read-only",
			"--cap-drop", "ALL",
			"--security-opt", "no-new-privileges",
			"--entrypoint", "/bin/busybox",
			probeImage,
			"wget", "--quiet", "--output-document=-", readinessURL,
		)
		if probeErr == nil {
			running, err = containerIsRunning(readinessCtx, runner, runtimeContainer)
			if err != nil {
				return err
			}
			if !running {
				return errors.New("container stopped as readiness was reported")
			}
			return nil
		}

		select {
		case <-readinessCtx.Done():
			return fmt.Errorf(
				"container did not become ready within %s: %w; last probe: %v: %s",
				timeout,
				readinessCtx.Err(),
				probeErr,
				bytes.TrimSpace(probeOutput),
			)
		case <-time.After(time.Second):
		}
	}
}

func containerIsRunning(ctx context.Context, runner commandRunner, container string) (bool, error) {
	output, err := runDocker(ctx, runner, "inspect", "--format", "{{.State.Running}}", container)
	if err != nil {
		return false, err
	}
	switch state := strings.TrimSpace(string(output)); state {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf("container running state is %q, want true or false", state)
	}
}

func removeContainerIfPresent(ctx context.Context, runner commandRunner, container string) error {
	present, err := containerExists(ctx, runner, container)
	if err != nil || !present {
		return err
	}
	_, err = runDocker(ctx, runner, "rm", "--force", container)
	return err
}

func containerExists(ctx context.Context, runner commandRunner, container string) (bool, error) {
	filter := "name=^/" + container + "$"
	output, err := runDocker(ctx, runner, "ps", "--all", "--quiet", "--filter", filter)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(output)) != "", nil
}

func runDocker(ctx context.Context, runner commandRunner, args ...string) ([]byte, error) {
	output, err := runner.Run(ctx, "docker", args...)
	if err != nil {
		return nil, &commandError{
			command: append([]string{"docker"}, args...),
			output:  output,
			cause:   err,
		}
	}
	return output, nil
}
