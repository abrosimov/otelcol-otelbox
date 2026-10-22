package contract

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const redactionProfile = `processors:
  redaction/secrets:
    allow_all_keys: true
    blocked_key_patterns:
      - '(?i)(^|.*[._-])(token|signature)($|[._-].*)'
      - 'it''s.quoted'
    # A comment between lists.
    ignored_keys:
      - llm.token_count.prompt
      - gen_ai.token.type
    blocked_values:
      - 'Bearer \S+'
  memory_limiter:
    check_interval: 1s
`

func writeTestFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadRedactionKeys(t *testing.T) {
	keys, err := ReadRedactionKeys(writeTestFile(t, "profile.yaml", redactionProfile))
	requireNoError(t, err)

	wantPatterns := []string{`(?i)(^|.*[._-])(token|signature)($|[._-].*)`, `it's.quoted`}
	if !slices.Equal(keys.BlockedKeyPatterns, wantPatterns) {
		t.Fatalf("BlockedKeyPatterns = %q, want %q", keys.BlockedKeyPatterns, wantPatterns)
	}
	wantIgnored := []string{"llm.token_count.prompt", "gen_ai.token.type"}
	if !slices.Equal(keys.IgnoredKeys, wantIgnored) {
		t.Fatalf("IgnoredKeys = %q, want %q", keys.IgnoredKeys, wantIgnored)
	}

	for name, content := range map[string]string{
		"no block":    "processors:\n  memory_limiter:\n    check_interval: 1s\n",
		"no patterns": "processors:\n  redaction/secrets:\n    allow_all_keys: true\n",
	} {
		if _, err := ReadRedactionKeys(writeTestFile(t, "profile.yaml", content)); err == nil {
			t.Fatalf("ReadRedactionKeys(%s) error = nil", name)
		}
	}
}

func TestExtractOpenInferenceKeys(t *testing.T) {
	keys, err := ExtractOpenInferenceKeys(strings.NewReader(`
class SpanAttributes:
    LLM_TOKEN_COUNT_PROMPT = "llm.token_count.prompt"
    """
    Number of tokens in the prompt.
    """
    OPENINFERENCE_SPAN_KIND = "openinference.span.kind"
    LLM = "LLM"
    LLM_TOKEN_COUNT_PROMPT_AGAIN = "llm.token_count.prompt"
`))
	requireNoError(t, err)
	want := []string{"llm.token_count.prompt", "openinference.span.kind"}
	if !slices.Equal(keys, want) {
		t.Fatalf("ExtractOpenInferenceKeys() = %q, want %q", keys, want)
	}
}

func TestExtractSemconvKeys(t *testing.T) {
	keys, err := ExtractSemconvKeys(strings.NewReader(`
attributes:
  - key: gen_ai.token.modality
    type:
      members:
        - id: text
          value: "text"
  - key: gen_ai.provider.name
groups:
  - id: registry.gen_ai.deprecated
    type: attribute_group
    attributes:
      - id: gen_ai.token.type
        type:
          members:
            - id: aws.bedrock
              value: "aws.bedrock"
      - id: gen_ai.usage.prompt_tokens
        type: int
`))
	requireNoError(t, err)
	want := []string{"gen_ai.provider.name", "gen_ai.token.modality", "gen_ai.token.type", "gen_ai.usage.prompt_tokens"}
	if !slices.Equal(keys, want) {
		t.Fatalf("ExtractSemconvKeys() = %q, want %q", keys, want)
	}
}

func TestReadRedactionExceptions(t *testing.T) {
	exceptions, err := ReadRedactionExceptions(writeTestFile(t, "exceptions.tsv",
		"# comment\n\nmessage_content.signature\tOpaque vendor signature.\n"))
	requireNoError(t, err)
	if exceptions["message_content.signature"] != "Opaque vendor signature." || len(exceptions) != 1 {
		t.Fatalf("ReadRedactionExceptions() = %v", exceptions)
	}
	if _, err := ReadRedactionExceptions(writeTestFile(t, "exceptions.tsv", "missing.reason\n")); err == nil {
		t.Fatal("ReadRedactionExceptions(no reason) error = nil")
	}
}

func TestCheckRedactionConventions(t *testing.T) {
	keys := RedactionKeys{
		BlockedKeyPatterns: []string{`(?i)(^|.*[._-])(token|signature)($|[._-].*)`},
		IgnoredKeys:        []string{"llm.token_count.prompt", "gen_ai.token.type"},
	}
	exceptions := map[string]string{"message_content.signature": "opaque"}
	conventions := []string{
		"llm.token_count.prompt", "gen_ai.token.type", "message_content.signature",
		"gen_ai.usage.input_tokens", "session.id",
	}

	var output bytes.Buffer
	requireNoError(t, CheckRedactionConventions(keys, conventions, exceptions, &output))
	if !strings.Contains(output.String(), "5 convention keys, 2 ignored, 1 recorded exceptions") {
		t.Fatalf("CheckRedactionConventions() output = %q", output.String())
	}

	tests := []struct {
		name        string
		keys        RedactionKeys
		conventions []string
		exceptions  map[string]string
		want        string
	}{
		{
			name:        "new masked convention key",
			keys:        keys,
			conventions: append(slices.Clone(conventions), "llm.token_count.total"),
			exceptions:  exceptions,
			want:        "convention key llm.token_count.total would be masked",
		},
		{
			name:        "stale ignored key",
			keys:        RedactionKeys{BlockedKeyPatterns: keys.BlockedKeyPatterns, IgnoredKeys: append(slices.Clone(keys.IgnoredKeys), "gone.token")},
			conventions: conventions,
			exceptions:  exceptions,
			want:        "ignored key gone.token is in none of the conventions",
		},
		{
			name:        "needless ignored key",
			keys:        RedactionKeys{BlockedKeyPatterns: keys.BlockedKeyPatterns, IgnoredKeys: append(slices.Clone(keys.IgnoredKeys), "session.id")},
			conventions: conventions,
			exceptions:  exceptions,
			want:        "ignored key session.id matches no blocked pattern",
		},
		{
			name:        "stale exception",
			keys:        keys,
			conventions: conventions,
			exceptions:  map[string]string{"message_content.signature": "opaque", "gone.signature": "opaque"},
			want:        "exception gone.signature is in none of the conventions",
		},
		{
			name:        "exception that is also ignored",
			keys:        keys,
			conventions: conventions,
			exceptions:  map[string]string{"message_content.signature": "opaque", "gen_ai.token.type": "both"},
			want:        "exception gen_ai.token.type is also an ignored key",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := CheckRedactionConventions(test.keys, test.conventions, test.exceptions, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("CheckRedactionConventions() error = %v, want %q", err, test.want)
			}
		})
	}
}
