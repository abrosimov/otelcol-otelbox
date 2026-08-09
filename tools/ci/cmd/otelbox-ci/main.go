package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/abrosimov/otelcol-otelbox/tools/ci/internal/contract"
	"github.com/abrosimov/otelcol-otelbox/tools/ci/internal/imagecheck"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "otelbox-ci: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: otelbox-ci shared check | binary check | image smoke | manifest resolve [options]")
	}

	switch args[0] + " " + args[1] {
	case "shared check":
		return runSharedCheck(args[2:], stdout)
	case "binary check":
		return runBinaryCheck(args[2:], stdout)
	case "image smoke":
		return runImageSmoke(args[2:], stdout)
	case "manifest resolve":
		return runManifestResolve(args[2:], stdout)
	default:
		return fmt.Errorf("unknown command %q", args[0]+" "+args[1])
	}
}

func runImageSmoke(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("image smoke", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	image := flags.String("image", "", "container image reference")
	config := flags.String("config", "", "image smoke configuration")
	probeImage := flags.String("probe-image", imagecheck.DefaultProbeImage, "readiness probe image")
	timeout := flags.Duration("timeout", 30*time.Second, "readiness timeout")
	pull := flags.Bool("pull", false, "pull the image reference before checking it")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("image smoke does not accept positional arguments")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return imagecheck.Smoke(ctx, imagecheck.Options{
		Image:      *image,
		ConfigPath: *config,
		ProbeImage: *probeImage,
		Timeout:    *timeout,
		Pull:       *pull,
	}, stdout)
}

type stringList []string

func (values *stringList) String() string {
	return fmt.Sprint([]string(*values))
}

func (values *stringList) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func runManifestResolve(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("manifest resolve", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	manifest := flags.String("manifest", "builder.yaml", "OCB manifest")
	var adaptations stringList
	var adaptationModules stringList
	flags.Var(&adaptations, "adaptation-version", "path to an adaptation UPSTREAM_VERSION file")
	flags.Var(&adaptationModules, "adaptation-module", "path to an adaptation go.mod file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("manifest resolve does not accept positional arguments")
	}

	manifestFile, err := os.Open(*manifest)
	if err != nil {
		return fmt.Errorf("open manifest: %w", err)
	}
	resolution, resolveErr := contract.ResolveManifest(manifestFile)
	closeErr := manifestFile.Close()
	if resolveErr != nil {
		return resolveErr
	}
	if closeErr != nil {
		return fmt.Errorf("close manifest: %w", closeErr)
	}
	if err := contract.CheckAdaptationVersions(resolution.UpstreamPin, adaptations); err != nil {
		return err
	}
	stablePin, err := contract.CheckAdaptationModules(resolution.UpstreamPin, adaptationModules)
	if err != nil {
		return err
	}

	fmt.Fprintf(stdout, "version=%s\n", resolution.Version)
	fmt.Fprintf(stdout, "upstream_pin=%s\n", resolution.UpstreamPin)
	fmt.Fprintf(stdout, "upstream_version=%s\n", resolution.UpstreamVersion)
	if stablePin != "" {
		fmt.Fprintf(stdout, "stable_pin=%s\n", stablePin)
	}
	return nil
}

func runSharedCheck(args []string, stdout io.Writer) error {
	profiles := args
	if len(profiles) == 0 {
		profiles = []string{"config/edge.yaml", "config/gateway.yaml", "config/host-agent.yaml"}
	}

	return contract.CheckSharedFiles(profiles, stdout)
}

func runBinaryCheck(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("binary check", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	binary := flags.String("binary", "", "collector binary")
	manifest := flags.String("manifest", "builder.yaml", "OCB manifest")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("binary check does not accept positional arguments")
	}
	if *binary == "" {
		return fmt.Errorf("binary check requires --binary")
	}

	manifestFile, err := os.Open(*manifest)
	if err != nil {
		return fmt.Errorf("open manifest: %w", err)
	}
	defer manifestFile.Close()

	command := exec.Command(*binary, "components")
	components, err := command.Output()
	if err != nil {
		return fmt.Errorf("run %s components: %w", *binary, err)
	}

	return contract.CheckBinary(manifestFile, components, stdout)
}
