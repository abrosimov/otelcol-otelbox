package spandedup

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"time"

	"go.uber.org/zap"

	"go.opentelemetry.io/collector/extension/xextension/storage"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

type spanKey [24]byte

func keyOf(span ptrace.Span) (spanKey, bool) {
	traceID := span.TraceID()
	spanID := span.SpanID()
	if traceID.IsEmpty() || spanID.IsEmpty() {
		return spanKey{}, false
	}
	var key spanKey
	copy(key[:16], traceID[:])
	copy(key[16:], spanID[:])
	return key, true
}

const (
	formatVersion    = 1
	metaStorageKey   = "meta"
	segmentKeyPrefix = "segment/"
	metaSize         = 1 + 8 + 8
	segmentHeader    = 1 + 8 + 8

	// Each append rewrites the open segment, so this bounds the write per
	// commit (24 KiB) as well as the size-eviction granularity.
	maxSegmentEntries = 1024
	// Entries live at least ttl and at most ttl plus one rotation age.
	rotationsPerTTL = 16
)

// Storage has no iteration, so the index persists as a FIFO of segments that
// rebuild the in-memory set at start; a segment is evicted whole, by age or size.
type segment struct {
	id     uint64
	oldest int64
	newest int64
	keys   []spanKey
}

type seenSet struct {
	client     storage.Client
	ttl        time.Duration
	rotateAge  time.Duration
	maxEntries int
	capacity   int

	index    map[spanKey]uint64
	segments []*segment
	nextID   uint64
	dirty    map[uint64]*segment
	deleted  []uint64
}

func newSeenSet(client storage.Client, ttl time.Duration, maxEntries int) *seenSet {
	return &seenSet{
		client:     client,
		ttl:        ttl,
		rotateAge:  ttl / rotationsPerTTL,
		maxEntries: maxEntries,
		capacity:   max(1, min(maxSegmentEntries, maxEntries/64)),
		index:      make(map[spanKey]uint64),
		dirty:      make(map[uint64]*segment),
	}
}

// loadSeenSet fails on a storage error, so the pipeline does not start without
// its history, but discards undecodable state: a corrupt index can only cause
// duplicates, while refusing to start would stop ingestion.
func loadSeenSet(ctx context.Context, client storage.Client, ttl time.Duration, maxEntries int, logger *zap.Logger) (*seenSet, error) {
	seen := newSeenSet(client, ttl, maxEntries)
	raw, err := client.Get(ctx, metaStorageKey)
	if err != nil {
		return nil, fmt.Errorf("reading the span deduplication index: %w", err)
	}
	if raw == nil {
		return seen, nil
	}
	first, next, err := decodeMeta(raw)
	if err != nil {
		logger.Warn("Discarding an unreadable span deduplication index", zap.Error(err))
		return seen, nil
	}
	// Each segment holds at least one entry, so a longer range is not state
	// this configuration retains; skipping it also bounds a corrupt range.
	if limit := uint64(maxEntries) + 1; next-first > limit {
		first = next - limit
	}
	seen.nextID = next
	for id := first; id < next; id++ {
		raw, err := client.Get(ctx, segmentStorageKey(id))
		if err != nil {
			return nil, fmt.Errorf("reading span deduplication segment %d: %w", id, err)
		}
		loaded, err := decodeSegment(id, raw)
		if err != nil {
			logger.Warn("Discarding an unreadable span deduplication segment", zap.Uint64("segment", id), zap.Error(err))
			seen.deleted = append(seen.deleted, id)
			continue
		}
		seen.segments = append(seen.segments, loaded)
		for _, key := range loaded.keys {
			seen.index[key] = id
		}
	}
	return seen, nil
}

func (s *seenSet) contains(key spanKey) bool {
	_, ok := s.index[key]
	return ok
}

// record keeps the in-memory set even when persisting fails: the spans are
// already in the next consumer, so dropping their retry is no loss, and the
// dirty segments are written again by the next successful commit.
func (s *seenSet) record(ctx context.Context, keys []spanKey, now time.Time) error {
	for _, key := range keys {
		if s.contains(key) {
			continue
		}
		open := s.openSegment(now)
		open.keys = append(open.keys, key)
		open.newest = now.UnixNano()
		s.index[key] = open.id
		s.dirty[open.id] = open
	}
	s.evict(now)
	return s.persist(ctx)
}

func (s *seenSet) openSegment(now time.Time) *segment {
	if n := len(s.segments); n > 0 {
		open := s.segments[n-1]
		if len(open.keys) < s.capacity && now.UnixNano()-open.oldest <= int64(s.rotateAge) {
			return open
		}
	}
	created := &segment{id: s.nextID, oldest: now.UnixNano(), newest: now.UnixNano()}
	s.nextID++
	s.segments = append(s.segments, created)
	return created
}

func (s *seenSet) evict(now time.Time) {
	horizon := now.Add(-s.ttl).UnixNano()
	for len(s.segments) > 1 {
		oldest := s.segments[0]
		if oldest.newest >= horizon && len(s.index) <= s.maxEntries {
			return
		}
		for _, key := range oldest.keys {
			if id, ok := s.index[key]; ok && id == oldest.id {
				delete(s.index, key)
			}
		}
		s.segments[0] = nil
		s.segments = s.segments[1:]
		delete(s.dirty, oldest.id)
		s.deleted = append(s.deleted, oldest.id)
	}
}

func (s *seenSet) persist(ctx context.Context) error {
	if len(s.dirty) == 0 && len(s.deleted) == 0 {
		return nil
	}
	ops := make([]*storage.Operation, 0, len(s.dirty)+len(s.deleted)+1)
	for id, dirty := range s.dirty {
		ops = append(ops, storage.SetOperation(segmentStorageKey(id), encodeSegment(dirty)))
	}
	for _, id := range s.deleted {
		ops = append(ops, storage.DeleteOperation(segmentStorageKey(id)))
	}
	first := s.nextID
	if len(s.segments) > 0 {
		first = s.segments[0].id
	}
	ops = append(ops, storage.SetOperation(metaStorageKey, encodeMeta(first, s.nextID)))
	if err := s.client.Batch(ctx, ops...); err != nil {
		return fmt.Errorf("persisting the span deduplication index: %w", err)
	}
	clear(s.dirty)
	s.deleted = s.deleted[:0]
	return nil
}

func segmentStorageKey(id uint64) string {
	return segmentKeyPrefix + strconv.FormatUint(id, 10)
}

func encodeMeta(first, next uint64) []byte {
	raw := make([]byte, metaSize)
	raw[0] = formatVersion
	binary.BigEndian.PutUint64(raw[1:9], first)
	binary.BigEndian.PutUint64(raw[9:17], next)
	return raw
}

func decodeMeta(raw []byte) (uint64, uint64, error) {
	if len(raw) != metaSize || raw[0] != formatVersion {
		return 0, 0, fmt.Errorf("index of %d bytes, version %d", len(raw), versionOf(raw))
	}
	first := binary.BigEndian.Uint64(raw[1:9])
	next := binary.BigEndian.Uint64(raw[9:17])
	if first > next {
		return 0, 0, fmt.Errorf("index range starts at %d after its end %d", first, next)
	}
	return first, next, nil
}

func encodeSegment(source *segment) []byte {
	raw := make([]byte, segmentHeader, segmentHeader+len(source.keys)*len(spanKey{}))
	raw[0] = formatVersion
	binary.BigEndian.PutUint64(raw[1:9], uint64(source.oldest))
	binary.BigEndian.PutUint64(raw[9:17], uint64(source.newest))
	for _, key := range source.keys {
		raw = append(raw, key[:]...)
	}
	return raw
}

func decodeSegment(id uint64, raw []byte) (*segment, error) {
	if raw == nil {
		return nil, errors.New("segment is missing")
	}
	keySize := len(spanKey{})
	if len(raw) < segmentHeader || raw[0] != formatVersion || (len(raw)-segmentHeader)%keySize != 0 {
		return nil, fmt.Errorf("segment of %d bytes, version %d", len(raw), versionOf(raw))
	}
	decoded := &segment{
		id:     id,
		oldest: int64(binary.BigEndian.Uint64(raw[1:9])),
		newest: int64(binary.BigEndian.Uint64(raw[9:17])),
		keys:   make([]spanKey, 0, (len(raw)-segmentHeader)/keySize),
	}
	for offset := segmentHeader; offset < len(raw); offset += keySize {
		decoded.keys = append(decoded.keys, spanKey(raw[offset:offset+keySize]))
	}
	return decoded, nil
}

func versionOf(raw []byte) int {
	if len(raw) == 0 {
		return -1
	}
	return int(raw[0])
}
