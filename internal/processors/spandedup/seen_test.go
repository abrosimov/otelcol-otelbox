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

func copiesOf(seen *seenSet, key spanKey) int {
	copies := 0
	for _, kept := range seen.segments {
		for _, candidate := range kept.keys {
			if candidate == key {
				copies++
			}
		}
	}
	return copies
}

func TestSeenSetRefreshKeepsARepeatedKeyBeyondSizeEviction(t *testing.T) {
	const maxEntries = 640
	client := newMemoryClient()
	seen := loadForTest(t, client, time.Hour, maxEntries)
	stuck := testKey(60000)
	require.NoError(t, seen.record(context.Background(), []spanKey{stuck}, epoch))

	// Fresh traffic would evict the key many times over; it is seen again after every batch.
	for batch := range 100 {
		require.NoError(t, seen.record(context.Background(), testKeys(batch*50, batch*50+50), epoch))
		require.True(t, seen.contains(stuck), "batch %d evicted a key that is still being re-sent", batch)
		seen.refresh(stuck, epoch)
		require.NoError(t, seen.commit(context.Background(), epoch))
		// One copy per half of the index, plus the one being replaced.
		require.LessOrEqual(t, copiesOf(seen, stuck), 4, "refresh must not copy a key into every segment")
	}
	require.LessOrEqual(t, len(seen.index), maxEntries)

	reloaded := loadForTest(t, client.reopen(), time.Hour, maxEntries)
	require.True(t, reloaded.contains(stuck), "a refreshed key must be persisted")
}

func TestSeenSetRefreshKeepsARepeatedKeyBeyondItsFirstTTL(t *testing.T) {
	const ttl = 16 * time.Hour
	seen := loadForTest(t, newMemoryClient(), ttl, 1000)
	stuck := testKey(1)
	require.NoError(t, seen.record(context.Background(), []spanKey{stuck}, epoch))

	for hour := 1; hour <= 48; hour++ {
		now := epoch.Add(time.Duration(hour) * time.Hour)
		require.NoError(t, seen.record(context.Background(), testKeys(1000+hour, 1001+hour), now))
		require.True(t, seen.contains(stuck), "hour %d expired a key that is still being re-sent", hour)
		seen.refresh(stuck, now)
		require.NoError(t, seen.commit(context.Background(), now))
	}

	// Once it stops arriving, it expires as any other key.
	later := epoch.Add(48*time.Hour + ttl + ttl/rotationsPerTTL + time.Second)
	require.NoError(t, seen.record(context.Background(), testKeys(2000, 2001), later))
	require.False(t, seen.contains(stuck))
}

func TestSeenSetRefreshLeavesNewerKeysInPlace(t *testing.T) {
	seen := loadForTest(t, newMemoryClient(), time.Hour, 640)
	require.NoError(t, seen.record(context.Background(), testKeys(0, 100), epoch))
	newest := testKey(99)
	require.False(t, seen.refresh(newest, epoch))
	require.Equal(t, 1, copiesOf(seen, newest))
	require.True(t, seen.refresh(testKey(0), epoch))
	require.False(t, seen.refresh(testKey(0), epoch), "a key just refreshed sits in the open segment")
}

func TestSeenSetWithoutStorageKeepsEntriesInMemory(t *testing.T) {
	seen := newSeenSet(nil, time.Hour, 640)
	require.NoError(t, seen.record(context.Background(), testKeys(0, 1000), epoch))
	require.True(t, seen.contains(testKey(999)))
	require.LessOrEqual(t, len(seen.index), 640)
	require.Empty(t, seen.dirty)
	require.Empty(t, seen.deleted)
}
