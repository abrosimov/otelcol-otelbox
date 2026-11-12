package contract

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var (
	semanticVersion  = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	pseudoVersionTag = regexp.MustCompile(`^-0\.[0-9]{14}-[0-9a-f]{12}$`)
)

type ManifestResolution struct {
	Version         string
	UpstreamPin     string
	UpstreamVersion string
	Replacements    []string
}

func ResolveManifest(input io.Reader) (ManifestResolution, error) {
	scanner := bufio.NewScanner(input)
	var versions []string
	var upstreamPins []string
	var componentPins []string
	var replacements [][]string

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
		if len(fields) > 1 && fields[0] == "-" && slices.Contains(fields, "=>") {
			replacements = append(replacements, fields[1:])
			continue
		}
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
	normalised := make([]string, 0, len(replacements))
	for _, replacement := range replacements {
		if err := checkReplacement(replacement, upstreamPin); err != nil {
			return ManifestResolution{}, err
		}
		normalised = append(normalised, strings.Join(replacement, " "))
	}

	return ManifestResolution{
		Version:         versions[0],
		UpstreamPin:     upstreamPin,
		UpstreamVersion: strings.TrimPrefix(upstreamPin, "v"),
		Replacements:    normalised,
	}, nil
}

// A version-form left side would stop applying silently once MVS selects another version.
func checkReplacement(fields []string, upstreamPin string) error {
	text := strings.Join(fields, " ")
	if len(fields) != 4 || fields[1] != "=>" {
		return fmt.Errorf("replacement %q must have the form <module> => <module> <version>", text)
	}
	if !strings.HasPrefix(fields[0], "go.opentelemetry.io/collector/") {
		return fmt.Errorf("replacement %q replaces a module outside the Collector core", text)
	}
	if !basedOnRelease(fields[3], upstreamPin) {
		return fmt.Errorf("replacement %q is not based on the upstream pin %s; re-base the fork", text, upstreamPin)
	}
	return nil
}

func basedOnRelease(version, release string) bool {
	if version == release {
		return true
	}
	parts := semanticVersion.FindStringSubmatch(strings.TrimPrefix(release, "v"))
	if parts == nil {
		return false
	}
	patch, err := strconv.Atoi(parts[3])
	if err != nil {
		return false
	}
	prefix := fmt.Sprintf("v%s.%s.%d", parts[1], parts[2], patch+1)
	tag, found := strings.CutPrefix(version, prefix)
	return found && pseudoVersionTag.MatchString(tag)
}

func CheckAdaptationReplacements(replacements []string, paths []string) error {
	for _, path := range paths {
		module, err := readModuleDirectives(path)
		if err != nil {
			return err
		}
		for _, replacement := range module.replacements {
			if !slices.Contains(replacements, replacement) {
				return fmt.Errorf("adaptation module %s carries replacement %q, which builder.yaml does not", path, replacement)
			}
		}
		for _, replacement := range replacements {
			replaced, _, _ := strings.Cut(replacement, " ")
			if slices.Contains(module.requirements, replaced) && !slices.Contains(module.replacements, replacement) {
				return fmt.Errorf("adaptation module %s requires %s without the builder.yaml replacement %q", path, replaced, replacement)
			}
		}
	}
	return nil
}

type moduleDirectives struct {
	requirements []string
	replacements []string
}

func readModuleDirectives(path string) (moduleDirectives, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return moduleDirectives{}, fmt.Errorf("read adaptation module %s: %w", path, err)
	}
	var directives moduleDirectives
	block := ""
	for line := range strings.Lines(string(content)) {
		fields := strings.Fields(line)
		switch {
		case len(fields) == 0:
			continue
		case fields[0] == ")":
			block = ""
			continue
		case len(fields) == 2 && fields[1] == "(":
			block = fields[0]
			continue
		}
		directive := block
		if directive == "" {
			directive, fields = fields[0], fields[1:]
		}
		switch directive {
		case "require":
			if len(fields) >= 2 {
				directives.requirements = append(directives.requirements, fields[0])
			}
		case "replace":
			directives.replacements = append(directives.replacements, strings.Join(fields, " "))
		}
	}
	return directives, nil
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
			line := scanner.Text()
			fields := strings.Fields(line)
			if len(fields) < 2 || !strings.HasPrefix(fields[0], "go.opentelemetry.io/collector") || slices.Contains(fields, "=>") {
				continue
			}
			// An indirect requirement is selected by an upstream module's own
			// go.mod, not declared here: v0.163.0 of otlpexporter still
			// requires the root collector module at v0.162.0. Only direct
			// requirements state which release a wrapper adapts.
			if strings.HasSuffix(strings.TrimSpace(line), "// indirect") {
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
