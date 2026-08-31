package contract

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"slices"
	"strings"
)

var componentKinds = []string{"receivers", "processors", "exporters", "extensions", "connectors"}

// Not the module comparison restated: a renamed type, or one resolving to a
// deprecated_type, still links its module at its pinned version, and only the
// name it registers shows that. One per manifest module, wired or not.
var requiredComponentNames = []string{
	"bearertokenauth",
	"file_storage",
	"filter",
	"headers_setter",
	"healthcheckv2",
	"host_metrics",
	"journald",
	"memory_limiter",
	"otlp",
	"otlp_grpc",
	"otlp_http",
	"prometheus",
	"redaction",
	"resource",
	"resource_detection",
	"transform",
	"file",
}

type componentModule struct {
	module  string
	version string
}

type manifestContract struct {
	counts  map[string]int
	modules []componentModule
}

type binaryContract struct {
	counts  map[string]int
	modules []componentModule
	names   []string
}

func CheckBinary(manifest io.Reader, components []byte, output io.Writer) error {
	declared, err := parseManifest(manifest)
	if err != nil {
		return err
	}
	linked, err := parseComponents(bytes.NewReader(components))
	if err != nil {
		return err
	}

	var failures []string
	for _, kind := range componentKinds {
		want := declared.counts[kind]
		got := linked.counts[kind]
		if want != got {
			failures = append(failures, fmt.Sprintf("%s: manifest declares %d, binary links %d", kind, want, got))
			continue
		}
		fmt.Fprintf(output, "binary check: %s: %d ok\n", kind, got)
	}

	for _, module := range declared.modules {
		if !slices.Contains(linked.modules, module) {
			failures = append(failures, fmt.Sprintf("declared module %s %s is absent", module.module, module.version))
		}
	}
	for _, name := range requiredComponentNames {
		if !slices.Contains(linked.names, name) {
			failures = append(failures, fmt.Sprintf("required component %s is absent", name))
		}
	}

	if len(failures) != 0 {
		return fmt.Errorf("built binary does not match manifest:\n  %s", strings.Join(failures, "\n  "))
	}
	fmt.Fprintln(output, "binary check: manifest modules and required component names are present")
	return nil
}

func parseManifest(input io.Reader) (manifestContract, error) {
	contract := manifestContract{counts: emptyCounts()}
	scanner := bufio.NewScanner(input)
	section := ""
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := scanner.Text()
		if !strings.HasPrefix(line, " ") && strings.HasSuffix(line, ":") {
			candidate := strings.TrimSuffix(line, ":")
			if slices.Contains(componentKinds, candidate) {
				section = candidate
			} else {
				section = ""
			}
			continue
		}
		if section == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 4 && fields[0] == "-" && fields[1] == "gomod:" {
			contract.counts[section]++
			contract.modules = append(contract.modules, componentModule{module: fields[2], version: fields[3]})
			continue
		}
		if strings.Contains(line, "gomod:") {
			return manifestContract{}, fmt.Errorf("manifest line %d has an invalid gomod declaration", lineNumber)
		}
	}
	if err := scanner.Err(); err != nil {
		return manifestContract{}, fmt.Errorf("read manifest: %w", err)
	}
	if len(contract.modules) == 0 {
		return manifestContract{}, fmt.Errorf("manifest declares no components")
	}
	return contract, nil
}

func parseComponents(input io.Reader) (binaryContract, error) {
	contract := binaryContract{counts: emptyCounts()}
	scanner := bufio.NewScanner(input)
	section := ""
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, " ") && strings.HasSuffix(line, ":") {
			candidate := strings.TrimSuffix(line, ":")
			if slices.Contains(componentKinds, candidate) {
				section = candidate
			} else {
				section = ""
			}
			continue
		}
		if section == "" {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- name: ") {
			name := strings.TrimSpace(strings.TrimPrefix(trimmed, "- name: "))
			if name == "" {
				return binaryContract{}, fmt.Errorf("binary components output contains an empty name")
			}
			contract.counts[section]++
			contract.names = append(contract.names, name)
			continue
		}
		if strings.HasPrefix(trimmed, "module: ") {
			fields := strings.Fields(strings.TrimPrefix(trimmed, "module: "))
			if len(fields) != 2 {
				return binaryContract{}, fmt.Errorf("binary components output contains an invalid module line %q", trimmed)
			}
			contract.modules = append(contract.modules, componentModule{module: fields[0], version: fields[1]})
		}
	}
	if err := scanner.Err(); err != nil {
		return binaryContract{}, fmt.Errorf("read binary components: %w", err)
	}
	if len(contract.names) == 0 {
		return binaryContract{}, fmt.Errorf("binary reports no components")
	}
	return contract, nil
}

func emptyCounts() map[string]int {
	counts := make(map[string]int, len(componentKinds))
	for _, kind := range componentKinds {
		counts[kind] = 0
	}
	return counts
}
