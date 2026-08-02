package harness

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The shell harness's 64/66 exit codes are deliberately not carried over: `go
// test` reports its own 1 for anything the test binary exits non-zero with, so
// a distinction here would be invisible to every caller. The stderr message is
// the whole contract.
// Ports are 34xxx throughout, chosen to miss every real deployment: the
// workstation edge holds 4317/4318/8888, the server gateway 14319/14320/8889
// and the host agent 8890. A developer running their own edge must be able to
// run this test.
const (
	edgeHTTPPort    = 34318
	edgeHealthPort  = 34133
	edgeMetricsPort = 34888

	gatewayHTTPPort = 34328
	gatewayGRPCPort = 34327

	// The coupling scenario runs its own gateway, on its own listeners, so a
	// process left behind by the delivery test cannot silently serve it.
	couplingGatewayHTTPPort    = 34348
	couplingGatewayMetricsPort = 34899

	healthyBackendEndpoint    = "127.0.0.1:34361"
	healthyBackendMetricsPort = 34891
	stalledBackendEndpoint    = "127.0.0.1:34362"
	stalledBackendMetricsPort = 34892
)

// Generous rather than tight. TLS startup, persistent queue recovery and the
// file sink all contend with a loaded CI runner; these values bound failure,
// not success, so a passing run normally touches none of them.
const (
	readyTimeout    = 45 * time.Second
	deliveryTimeout = 90 * time.Second
	failureTimeout  = 90 * time.Second

	// A stuck collector holding 34318 would break the *next* run rather than
	// this one, so SIGTERM is given a deadline of its own.
	stopTimeout  = 10 * time.Second
	pollInterval = 250 * time.Millisecond
)

var binaryFlag = flag.String("otelcol-binary", "",
	"path to the otelcol-otelbox binary under test (required)")

var (
	binaryPath string
	repoRoot   string
)

func TestMain(m *testing.M) {
	flag.Parse()

	if *binaryFlag == "" {
		// `-C` is not a flourish: this is a module of its own and the repository
		// root has no go.mod, so `go test ./test/harness` from the root fails to
		// resolve a module before it runs anything. Whoever reads this message
		// is by definition someone who does not yet know how to invoke the
		// harness, so it must not hand them a command that cannot work.
		fmt.Fprintln(os.Stderr,
			"usage, from the repository root:\n"+
				"  go test -C test/harness . -args -otelcol-binary <path-to-otelcol-otelbox>")
		os.Exit(1)
	}
	if err := resolveBinary(); err != nil {
		fmt.Fprintf(os.Stderr, "harness: %v\n", err)
		os.Exit(1)
	}
	if err := resolveRepoRoot(); err != nil {
		fmt.Fprintf(os.Stderr, "harness: %v\n", err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

func resolveBinary() error {
	path, err := filepath.Abs(*binaryFlag)
	if err != nil {
		return fmt.Errorf("resolving %q: %w", *binaryFlag, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stating the binary under test: %w", err)
	}
	if info.IsDir() || info.Mode().Perm()&0o111 == 0 {
		// Named rather than left to be worked out: an artefact download loses
		// the executable bit, so this is what a CI job that forgot `chmod +x`
		// sees, and it is the only thing wrong with it.
		return fmt.Errorf("%q is not an executable — an artefact download loses the mode, so `chmod +x` it first", path)
	}
	binaryPath = path
	return nil
}

func resolveRepoRoot() error {
	// `go test` runs with the package directory as the working directory, so the
	// repository root is two levels up. The role profiles are what get checked
	// rather than the directory itself: each is the first of the two configs a
	// collector under test composes, and a checkout missing one should fail here
	// with a sentence rather than inside a config loader.
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		return fmt.Errorf("resolving the repository root: %w", err)
	}
	for _, role := range []string{"edge.yaml", "gateway.yaml"} {
		profile := filepath.Join(root, "config", role)
		if _, err := os.Stat(profile); err != nil {
			return fmt.Errorf("expected the %s role profile at %s: %w", role, profile, err)
		}
	}
	repoRoot = root
	return nil
}

func configPath(parts ...string) string {
	return filepath.Join(append([]string{repoRoot}, parts...)...)
}

// Freshly generated every run. A checked-in token would still be a credential
// in a repository, and a fixed one could match a value left over from an
// earlier run and let a broken build pass.
func randomHex(t *testing.T, n int) string {
	t.Helper()

	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("could not read %d random bytes for a per-run credential: %v", n, err)
	}
	return hex.EncodeToString(buf)
}
