package contract

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

var semanticVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

type ManifestResolution struct {
	Version         string
	UpstreamPin     string
	UpstreamVersion string
}

func ResolveManifest(input io.Reader) (ManifestResolution, error) {
	scanner := bufio.NewScanner(input)
	var versions []string
	var upstreamPins []string
	var componentPins []string

	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "  version:") {
			fields := strings.Fields(line)
			if len(fields) != 2 || fields[0] != "version:" {
				return ManifestResolution{}, fmt.Errorf("dist.version must be one unquoted scalar")
			}
			versions = append(versions, fields[1])
		}

		fields := strings.Fields(line)
		if len(fields) != 4 || fields[0] != "-" || fields[1] != "gomod:" {
			continue
		}
		componentPins = append(componentPins, fields[3])
		if fields[2] == "go.opentelemetry.io/collector/receiver/otlpreceiver" {
			upstreamPins = append(upstreamPins, fields[3])
		}
	}
	if err := scanner.Err(); err != nil {
		return ManifestResolution{}, fmt.Errorf("read manifest: %w", err)
	}
	if len(versions) != 1 {
		return ManifestResolution{}, fmt.Errorf("manifest must contain exactly one dist.version, got %d", len(versions))
	}
	if !semanticVersion.MatchString(versions[0]) {
		return ManifestResolution{}, fmt.Errorf("dist.version must be an unquoted stable semantic version, got %q", versions[0])
	}
	if len(upstreamPins) != 1 {
		return ManifestResolution{}, fmt.Errorf("manifest must contain exactly one otlpreceiver pin, got %d", len(upstreamPins))
	}
	upstreamPin := upstreamPins[0]
	if !strings.HasPrefix(upstreamPin, "v") || !semanticVersion.MatchString(strings.TrimPrefix(upstreamPin, "v")) {
		return ManifestResolution{}, fmt.Errorf("otlpreceiver pin must be a stable semantic version prefixed with v, got %q", upstreamPin)
	}
	for _, pin := range componentPins {
		if pin != upstreamPin {
			return ManifestResolution{}, fmt.Errorf("manifest mixes component pins %s and %s", upstreamPin, pin)
		}
	}

	return ManifestResolution{
		Version:         versions[0],
		UpstreamPin:     upstreamPin,
		UpstreamVersion: strings.TrimPrefix(upstreamPin, "v"),
	}, nil
}

func CheckAdaptationVersions(upstreamPin string, paths []string) error {
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read adaptation version %s: %w", path, err)
		}
		actual := strings.TrimSpace(string(content))
		if actual != upstreamPin {
			return fmt.Errorf("adaptation %s is based on %s, not %s", path, actual, upstreamPin)
		}
	}
	return nil
}

func CheckAdaptationModules(upstreamPin string, paths []string) (string, error) {
	stablePin := ""
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			return "", fmt.Errorf("open adaptation module %s: %w", path, err)
		}
		scanner := bufio.NewScanner(file)
		collectorDependencies := 0
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) < 2 || !strings.HasPrefix(fields[0], "go.opentelemetry.io/collector") {
				continue
			}
			collectorDependencies++
			pin := fields[1]
			switch {
			case strings.HasPrefix(pin, "v0."):
				if pin != upstreamPin {
					file.Close()
					return "", fmt.Errorf("adaptation module %s mixes %s with %s", path, upstreamPin, pin)
				}
			case strings.HasPrefix(pin, "v1."):
				if stablePin == "" {
					stablePin = pin
				} else if pin != stablePin {
					file.Close()
					return "", fmt.Errorf("adaptation modules mix stable Collector pins %s and %s", stablePin, pin)
				}
			default:
				file.Close()
				return "", fmt.Errorf("adaptation module %s has unsupported Collector pin %s", path, pin)
			}
		}
		scanErr := scanner.Err()
		closeErr := file.Close()
		if scanErr != nil {
			return "", fmt.Errorf("read adaptation module %s: %w", path, scanErr)
		}
		if closeErr != nil {
			return "", fmt.Errorf("close adaptation module %s: %w", path, closeErr)
		}
		if collectorDependencies == 0 {
			return "", fmt.Errorf("adaptation module %s declares no Collector dependencies", path)
		}
	}
	if len(paths) != 0 && stablePin == "" {
		return "", fmt.Errorf("adaptation modules declare no stable Collector dependencies")
	}
	return stablePin, nil
}
