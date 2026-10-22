package contract

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// RedactionKeys is the key-level part of a role profile's redaction/secrets
// block: the patterns that mask a whole value and the exact keys exempt from
// them.
type RedactionKeys struct {
	BlockedKeyPatterns []string
	IgnoredKeys        []string
}

// ReadRedactionKeys reads blocked_key_patterns and ignored_keys from the
// redaction/secrets block of a role profile. The profiles write both as block
// sequences of plain or single-quoted scalars, which is all this reads.
func ReadRedactionKeys(path string) (RedactionKeys, error) {
	file, err := os.Open(path)
	if err != nil {
		return RedactionKeys{}, fmt.Errorf("open profile %s: %w", path, err)
	}
	defer file.Close()

	var keys RedactionKeys
	var list *[]string
	inBlock := false
	blockIndent, listIndent := -1, -1
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if trimmed == "redaction/secrets:" {
			inBlock, blockIndent = true, indent
			continue
		}
		if !inBlock {
			continue
		}
		if indent <= blockIndent {
			break
		}
		if list != nil && indent > listIndent && strings.HasPrefix(trimmed, "- ") {
			*list = append(*list, unquoteScalar(strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))))
			continue
		}
		list = nil
		switch trimmed {
		case "blocked_key_patterns:":
			list, listIndent = &keys.BlockedKeyPatterns, indent
		case "ignored_keys:":
			list, listIndent = &keys.IgnoredKeys, indent
		}
	}
	if err := scanner.Err(); err != nil {
		return RedactionKeys{}, fmt.Errorf("read profile %s: %w", path, err)
	}
	if !inBlock {
		return RedactionKeys{}, fmt.Errorf("profile %s has no redaction/secrets block", path)
	}
	if len(keys.BlockedKeyPatterns) == 0 {
		return RedactionKeys{}, fmt.Errorf("profile %s declares no blocked_key_patterns", path)
	}
	return keys, nil
}

func unquoteScalar(value string) string {
	if len(value) >= 2 && strings.HasPrefix(value, "'") && strings.HasSuffix(value, "'") {
		return strings.ReplaceAll(value[1:len(value)-1], "''", "'")
	}
	return value
}

var (
	pythonConstant = regexp.MustCompile(`=\s*"([a-z][a-z0-9_]*(?:\.[a-z0-9_]+)+)"`)
	semconvKey     = regexp.MustCompile(`^\s*- key:\s*([a-z][a-z0-9_]*(?:\.[a-z0-9_]+)+)\s*$`)
	semconvID      = regexp.MustCompile(`^\s*- id:\s*([a-z][a-z0-9_]*(?:\.[a-z0-9_]+)+)\s*$`)
	yamlListKey    = regexp.MustCompile(`^\s*([a-z_]+):\s*$`)
)

// ExtractOpenInferenceKeys returns the dotted attribute names assigned to
// constants in the OpenInference Python semantic-conventions module.
func ExtractOpenInferenceKeys(input io.Reader) ([]string, error) {
	content, err := io.ReadAll(input)
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, match := range pythonConstant.FindAllSubmatch(content, -1) {
		keys = append(keys, string(match[1]))
	}
	return sortedUnique(keys), nil
}

// ExtractSemconvKeys returns the attribute names a semantic-conventions
// registry declares: `- key:` entries in the current format and, in the older
// format, `- id:` entries directly under an `attributes:` list, so enum
// member ids such as provider names are not mistaken for attributes.
func ExtractSemconvKeys(input io.Reader) ([]string, error) {
	type list struct {
		indent int
		kind   string
	}
	var stack []list
	var keys []string
	scanner := bufio.NewScanner(input)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		for len(stack) > 0 && stack[len(stack)-1].indent >= indent {
			stack = stack[:len(stack)-1]
		}
		if match := semconvKey.FindStringSubmatch(line); match != nil {
			keys = append(keys, match[1])
			continue
		}
		if match := semconvID.FindStringSubmatch(line); match != nil {
			if len(stack) > 0 && stack[len(stack)-1].kind == "attributes" {
				keys = append(keys, match[1])
			}
			continue
		}
		if match := yamlListKey.FindStringSubmatch(line); match != nil {
			stack = append(stack, list{indent: indent, kind: match[1]})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return sortedUnique(keys), nil
}

// ReadRedactionExceptions reads the convention keys deliberately left to the
// blocked-key patterns: one `key<TAB>reason` per line, `#` comments allowed.
func ReadRedactionExceptions(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open exceptions %s: %w", path, err)
	}
	defer file.Close()

	exceptions := map[string]string{}
	scanner := bufio.NewScanner(file)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		key, reason, found := strings.Cut(text, "\t")
		if !found || strings.TrimSpace(reason) == "" {
			return nil, fmt.Errorf("%s:%d: want key, a tab and a reason", path, line)
		}
		exceptions[strings.TrimSpace(key)] = strings.TrimSpace(reason)
	}
	return exceptions, scanner.Err()
}

// CheckRedactionConventions holds a profile's key-level redaction to the
// attribute names of the semantic conventions producers use. Every convention
// key a blocked pattern would mask must be either an ignored key or a recorded
// exception, and every ignored key and exception must still be a convention
// key the patterns would mask; otherwise the list has drifted from the
// conventions in one direction or the other.
func CheckRedactionConventions(keys RedactionKeys, conventions []string, exceptions map[string]string, output io.Writer) error {
	patterns := make([]*regexp.Regexp, 0, len(keys.BlockedKeyPatterns))
	for _, pattern := range keys.BlockedKeyPatterns {
		compiled, err := regexp.Compile("^(?:" + pattern + ")$")
		if err != nil {
			return fmt.Errorf("blocked key pattern %q: %w", pattern, err)
		}
		patterns = append(patterns, compiled)
	}
	blocked := func(key string) bool {
		return slices.ContainsFunc(patterns, func(pattern *regexp.Regexp) bool { return pattern.MatchString(key) })
	}

	known := map[string]bool{}
	for _, key := range conventions {
		known[key] = true
	}
	ignored := map[string]bool{}
	for _, key := range keys.IgnoredKeys {
		ignored[key] = true
	}

	var problems []string
	for _, key := range sortedUnique(conventions) {
		_, excepted := exceptions[key]
		if blocked(key) && !ignored[key] && !excepted {
			problems = append(problems, fmt.Sprintf("convention key %s would be masked: add it to ignored_keys or record it as an exception", key))
		}
	}
	for _, key := range sortedUnique(keys.IgnoredKeys) {
		switch {
		case !known[key]:
			problems = append(problems, fmt.Sprintf("ignored key %s is in none of the conventions checked", key))
		case !blocked(key):
			problems = append(problems, fmt.Sprintf("ignored key %s matches no blocked pattern and need not be ignored", key))
		}
	}
	for _, key := range sortedKeys(exceptions) {
		switch {
		case !known[key]:
			problems = append(problems, fmt.Sprintf("exception %s is in none of the conventions checked", key))
		case !blocked(key):
			problems = append(problems, fmt.Sprintf("exception %s matches no blocked pattern", key))
		case ignored[key]:
			problems = append(problems, fmt.Sprintf("exception %s is also an ignored key", key))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("redaction keys drifted from the semantic conventions:\n  %s", strings.Join(problems, "\n  "))
	}
	fmt.Fprintf(output, "redaction check: %d convention keys, %d ignored, %d recorded exceptions\n",
		len(known), len(keys.IgnoredKeys), len(exceptions))
	return nil
}

func sortedUnique(values []string) []string {
	sorted := slices.Clone(values)
	sort.Strings(sorted)
	return slices.Compact(sorted)
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
