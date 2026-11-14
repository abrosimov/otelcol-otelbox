package otlpauthretry

// Kept identical in both wrapper modules: a shared local module would need a
// `replace` in builder.yaml, which this repository forbids.

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/klauspost/compress/zstd"

	"go.opentelemetry.io/collector/config/configoptional"
	"go.opentelemetry.io/collector/exporter/exporterhelper"
	"go.opentelemetry.io/collector/exporter/exporterhelper/xexporterhelper"
)

// StorageCompression selects how the persistent queue stores its items.
type StorageCompression string

const (
	// StorageCompressionNone stores items as the upstream encoding marshals
	// them. The empty value means the same.
	StorageCompressionNone StorageCompression = "none"
	// StorageCompressionZstd stores every new item as one zstd frame.
	StorageCompressionZstd StorageCompression = "zstd"
)

// Validate checks that the value names a supported compression.
func (c StorageCompression) Validate() error {
	switch c {
	case "", StorageCompressionNone, StorageCompressionZstd:
		return nil
	default:
		return fmt.Errorf("unsupported storage compression %q", string(c))
	}
}

// Enabled reports whether new items are compressed.
func (c StorageCompression) Enabled() bool {
	return c == StorageCompressionZstd
}

func validateStorageCompression(c StorageCompression, queue configoptional.Optional[exporterhelper.QueueBatchConfig]) error {
	if c.Enabled() && (!queue.HasValue() || queue.Get().StorageID == nil) {
		return errors.New(`"storage_compression" requires "sending_queue.storage"`)
	}
	return nil
}

// queueSettings wraps the upstream encoding. The wrapper always reads zstd
// frames, so a queue written with compression is still delivered once it is
// turned off, and compresses new items only when it is enabled. The forked
// exporterhelper counts a persistent item at the bytes it stores, so under
// `sizer: bytes` the queue then bounds compressed bytes.
func queueSettings(base xexporterhelper.QueueBatchSettings, c StorageCompression) xexporterhelper.QueueBatchSettings {
	base.Encoding = zstdEncoding{inner: base.Encoding, compress: c.Enabled()}
	return base
}

// The four bytes that open every zstd frame. A request encoded by pdata starts
// with the 0x0D tag of its format version, so a frame and an uncompressed item
// cannot be confused, and a queue reads both whatever the setting is.
var zstdFrameMagic = []byte{0x28, 0xB5, 0x2F, 0xFD}

// maxDecodedItemSize bounds the memory one decompressed item may take, so a
// corrupt frame header cannot allocate without limit.
const maxDecodedItemSize = 1 << 30

var (
	zstdEncoder = mustZstdEncoder()
	zstdDecoder = mustZstdDecoder()
)

func mustZstdEncoder() *zstd.Encoder {
	// EncodeAll is safe for concurrent use.
	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
	if err != nil {
		panic(err)
	}
	return encoder
}

func mustZstdDecoder() *zstd.Decoder {
	// DecodeAll is safe for concurrent use.
	decoder, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(0), zstd.WithDecoderMaxMemory(maxDecodedItemSize))
	if err != nil {
		panic(err)
	}
	return decoder
}

type zstdEncoding struct {
	inner    exporterhelper.QueueBatchEncoding[xexporterhelper.Request]
	compress bool
}

func (e zstdEncoding) Marshal(ctx context.Context, req xexporterhelper.Request) ([]byte, error) {
	encoded, err := e.inner.Marshal(ctx, req)
	if err != nil || !e.compress {
		return encoded, err
	}
	return zstdEncoder.EncodeAll(encoded, make([]byte, 0, len(encoded)/4)), nil
}

// Unmarshal reads a compressed or an uncompressed item, so items stored before
// compression was turned on, or after it was turned off, are still delivered.
// Bytes that open with the magic but do not decode go to the upstream encoding
// unchanged, which then rejects a corrupt item as it would any other.
func (e zstdEncoding) Unmarshal(stored []byte) (context.Context, xexporterhelper.Request, error) {
	return e.inner.Unmarshal(decompressStoredItem(stored))
}

func decompressStoredItem(stored []byte) []byte {
	if !bytes.HasPrefix(stored, zstdFrameMagic) {
		return stored
	}
	decoded, err := zstdDecoder.DecodeAll(stored, nil)
	if err != nil {
		return stored
	}
	return decoded
}
