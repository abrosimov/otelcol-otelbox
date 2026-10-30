package spandedup

import (
	"context"
	"maps"
	"sync"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/extension/xextension/storage"
)

type memoryClient struct {
	mu       sync.Mutex
	values   map[string][]byte
	getErr   error
	batchErr error
	closed   bool
}

func newMemoryClient() *memoryClient {
	return &memoryClient{values: make(map[string][]byte)}
}

func (c *memoryClient) Get(_ context.Context, key string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.getErr != nil {
		return nil, c.getErr
	}
	value, ok := c.values[key]
	if !ok {
		return nil, nil
	}
	return append([]byte(nil), value...), nil
}

func (c *memoryClient) Set(ctx context.Context, key string, value []byte) error {
	return c.Batch(ctx, storage.SetOperation(key, value))
}

func (c *memoryClient) Delete(ctx context.Context, key string) error {
	return c.Batch(ctx, storage.DeleteOperation(key))
}

func (c *memoryClient) Batch(_ context.Context, ops ...*storage.Operation) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.batchErr != nil {
		return c.batchErr
	}
	for _, op := range ops {
		switch op.Type {
		case storage.Set:
			c.values[op.Key] = append([]byte(nil), op.Value...)
		case storage.Delete:
			delete(c.values, op.Key)
		case storage.Get:
			op.Value = append([]byte(nil), c.values[op.Key]...)
		}
	}
	return nil
}

func (c *memoryClient) Close(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

func (c *memoryClient) snapshot() map[string][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return maps.Clone(c.values)
}

func (c *memoryClient) failBatches(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.batchErr = err
}

func (c *memoryClient) reopen() *memoryClient {
	return &memoryClient{values: c.snapshot()}
}

type memoryExtension struct {
	component.StartFunc
	component.ShutdownFunc
	client  *memoryClient
	openErr error
}

func (e *memoryExtension) GetClient(context.Context, component.Kind, component.ID, string) (storage.Client, error) {
	if e.openErr != nil {
		return nil, e.openErr
	}
	return e.client, nil
}

type extensionHost map[component.ID]component.Component

func (h extensionHost) GetExtensions() map[component.ID]component.Component {
	return h
}
