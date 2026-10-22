package harness

import (
	"testing"
)

// TestEdgeReportsStorageVolumeSpace holds PC-04's macOS-capable signal: the
// edge's host_metrics/storage receiver reports filesystem usage and
// utilisation, and those points reach the gateway through the edge's own
// self-telemetry pipeline, where a deployment can alert on the mount point that
// holds the WAL.
func TestEdgeReportsStorageVolumeSpace(t *testing.T) {
	pair := startEdgeAndGateway(t, "storage-volume")

	for _, metric := range []string{`"system.filesystem.usage"`, `"system.filesystem.utilization"`} {
		if err := poll(deliveryTimeout, pair.edge, metric+" to reach the gateway", func() bool {
			return sinkContains(t, pair.sink, metric)
		}); err != nil {
			t.Fatalf("the edge never delivered %s from host_metrics/storage: %v", metric, err)
		}
	}
	if !sinkContains(t, pair.sink, `"mountpoint"`) {
		t.Fatalf("filesystem points arrived without the mountpoint attribute an alert selects the WAL volume by")
	}
}
