package harness

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestHostAgentStartsOnLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("journald and host metrics require Linux")
	}

	state := t.TempDir()
	authHeader := writeAuthHeaderFile(t, filepath.Join(state, "auth-header"), "host-agent-smoke")
	hostAgent := startCollector(t, collectorSpec{
		name:     "host-agent",
		stateDir: state,
		env: map[string]string{
			"OTELBOX_STORAGE_DIR":               filepath.Join(state, "storage"),
			"OTELBOX_UPSTREAM_ENDPOINT":         "127.0.0.1:34999",
			"OTELBOX_UPSTREAM_AUTH_HEADER_FILE": authHeader,
			"OTELBOX_SCRAPE_TARGET_1":           "127.0.0.1:34998",
			"OTELBOX_SCRAPE_TARGET_2":           "127.0.0.1:34997",
			"OTELBOX_JOURNAL_UNIT_1":            "systemd-journald.service",
			"OTELBOX_JOURNAL_UNIT_2":            "cron.service",
			"OTELBOX_AUTH_RETRY_INTERVAL":       "200ms",
			"OTELBOX_STORAGE_MAX_SIZE_BYTES":    "67108864",
			"OTELBOX_QUEUE_SIZE_BYTES":          "33554432",
			"OTELBOX_EXPORTER_CONSUMERS":        "1",
		},
		configs: []string{
			configPath("config", "host-agent.yaml"),
			configPath("test", "config", "host-agent-ci.yaml"),
		},
	})

	defer func() {
		if t.Failed() {
			dumpDiagnostics(t, []diagnosticSection{{"host-agent metrics", scrapeMetrics(hostAgentMetricsPort)}}, hostAgent)
		}
	}()

	if err := poll(readyTimeout, hostAgent, "the host-agent health endpoint", func() bool {
		return healthEndpointReady(hostAgentHealthPort)
	}); err != nil {
		t.Fatalf("host-agent never became healthy: %v", err)
	}
	if err := poll(readyTimeout, hostAgent, "the host-agent metrics endpoint", func() bool {
		return strings.Contains(scrapeMetrics(hostAgentMetricsPort), "otelcol_process_")
	}); err != nil {
		t.Fatalf("host-agent did not publish process metrics: %v", err)
	}

	t.Log("host-agent started with journald, host metrics and self telemetry on Linux; upstream outage remains queued at 127.0.0.1:34999")
}
