package spandedup

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

var epoch = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

func testKey(n int) spanKey {
	var key spanKey
	key[0] = 1
	key[16] = 1
	key[22] = byte(n >> 8)
	key[23] = byte(n)
	return key
}

func testKeys(from, to int) []spanKey {
	keys := make([]spanKey, 0, to-from)
	for n := from; n < to; n++ {
		keys = append(keys, testKey(n))
	}
	return keys
}

func loadForTest(t *testing.T, client *memoryClient, ttl time.Duration, maxEntries int) *seenSet {
	t.Helper()
	seen, err := loadSeenSet(context.Background(), client, ttl, maxEntries, zap.NewNop())
	require.NoError(t, err)
	return seen
}

func TestSeenSetSurvivesReload(t *testing.T) {
	client := newMemoryClient()
	seen := loadForTest(t, client, time.Hour, 1000)
	require.NoError(t, seen.record(context.Background(), testKeys(0, 3), epoch))

	reloaded := loadForTest(t, client.reopen(), time.Hour, 1000)

	for _, key := range testKeys(0, 3) {
		require.True(t, reloaded.contains(key))
	}
	require.False(t, reloaded.contains(testKey(3)))
}

func TestSeenSetKeepsEntriesForAtLeastTTL(t *testing.T) {
	const ttl = 16 * time.Hour
	client := newMemoryClient()
	seen := loadForTest(t, client, ttl, 1000)
	require.NoError(t, seen.record(context.Background(), testKeys(0, 1), epoch))

	require.NoError(t, seen.record(context.Background(), testKeys(1, 2), epoch.Add(ttl)))
	require.True(t, seen.contains(testKey(0)), "an entry exactly ttl old must still be retained")

	require.NoError(t, seen.record(context.Background(), testKeys(2, 3), epoch.Add(ttl+ttl/rotationsPerTTL+time.Second)))
	require.False(t, seen.contains(testKey(0)), "an entry older than ttl plus one rotation age must be evicted")
	require.True(t, seen.contains(testKey(1)))

	reloaded := loadForTest(t, client.reopen(), ttl, 1000)
	require.False(t, reloaded.contains(testKey(0)), "eviction must be persisted")
	require.True(t, reloaded.contains(testKey(1)))
	require.True(t, reloaded.contains(testKey(2)))
}

func TestSeenSetEvictsOldestBeyondMaxEntries(t *testing.T) {
	const maxEntries = 640
	client := newMemoryClient()
	seen := loadForTest(t, client, time.Hour, maxEntries)
	require.Equal(t, 10, seen.capacity)

	require.NoError(t, seen.record(context.Background(), testKeys(0, 1000), epoch))

	require.LessOrEqual(t, len(seen.index), maxEntries)
	require.GreaterOrEqual(t, len(seen.index), maxEntries-seen.capacity)
	require.False(t, seen.contains(testKey(0)))
	require.True(t, seen.contains(testKey(999)))

	reloaded := loadForTest(t, client.reopen(), time.Hour, maxEntries)
	require.Equal(t, len(seen.index), len(reloaded.index))
}

func TestSeenSetKeepsUnpersistedEntriesAndWritesThemLater(t *testing.T) {
	client := newMemoryClient()
	seen := loadForTest(t, client, time.Hour, 1000)
	storageDown := errors.New("disk unavailable")
	client.failBatches(storageDown)

	err := seen.record(context.Background(), testKeys(0, 2), epoch)
	require.ErrorIs(t, err, storageDown)
	require.True(t, seen.contains(testKey(0)), "the in-memory set must keep spans the next consumer already accepted")

	client.failBatches(nil)
	require.NoError(t, seen.record(context.Background(), testKeys(2, 3), epoch))

	reloaded := loadForTest(t, client.reopen(), time.Hour, 1000)
	for _, key := range testKeys(0, 3) {
		require.True(t, reloaded.contains(key), "a later commit must persist entries an earlier one failed to write")
	}
}

func TestLoadSeenSetDiscardsUnreadableState(t *testing.T) {
	client := newMemoryClient()
	seen := loadForTest(t, client, time.Hour, 640)
	require.NoError(t, seen.record(context.Background(), testKeys(0, 25), epoch))
	client.values[segmentStorageKey(0)] = []byte("not a segment")

	reloaded := loadForTest(t, client, time.Hour, 640)
	require.False(t, reloaded.contains(testKey(0)))
	require.True(t, reloaded.contains(testKey(24)))

	client.values[metaStorageKey] = []byte{formatVersion + 1}
	require.Empty(t, loadForTest(t, client, time.Hour, 640).index)
}

func TestLoadSeenSetFailsOnStorageError(t *testing.T) {
	client := newMemoryClient()
	client.getErr = errors.New("disk unavailable")

	_, err := loadSeenSet(context.Background(), client, time.Hour, 1000, zap.NewNop())

	require.ErrorIs(t, err, client.getErr)
}

func TestSegmentEncodingRoundTrips(t *testing.T) {
	original := &segment{id: 7, oldest: epoch.UnixNano(), newest: epoch.Add(time.Minute).UnixNano(), keys: testKeys(0, 3)}

	decoded, err := decodeSegment(7, encodeSegment(original))

	require.NoError(t, err)
	require.Equal(t, original, decoded)
}
