package mocks

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/dapr/go-sdk/client"
)

// FakeStateStore is an in-memory stand-in for the Dapr state API.
// It keeps per-store key/value data and records the metadata of each save,
// so tests can exercise code that reads back what it wrote.
// Setting one of the *Err fields makes the matching call fail with that error.
type FakeStateStore struct {
	mu   sync.Mutex
	data map[string]map[string][]byte
	meta map[string]map[string]string
	// bulkGets and bulkDeletes hold the number of keys of each bulk call.
	bulkGets    []int
	bulkDeletes []int

	GetErr        error
	SaveErr       error
	BulkGetErr    error
	BulkDeleteErr error
}

// NewFakeStateStore returns an empty FakeStateStore.
func NewFakeStateStore() *FakeStateStore {
	return &FakeStateStore{
		data: make(map[string]map[string][]byte),
		meta: make(map[string]map[string]string),
	}
}

// SaveState stores a copy of data under key.
func (f *FakeStateStore) SaveState(_ context.Context, storeName, key string, data []byte, meta map[string]string, _ ...client.StateOption) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.SaveErr != nil {
		return f.SaveErr
	}
	if f.data[storeName] == nil {
		f.data[storeName] = make(map[string][]byte)
	}
	f.data[storeName][key] = slices.Clone(data)
	f.meta[storeName+"/"+key] = maps.Clone(meta)
	return nil
}

// GetState returns the value under key, or an item with an empty value when the key is absent,
// as the Dapr API does.
func (f *FakeStateStore) GetState(_ context.Context, storeName, key string, _ map[string]string) (*client.StateItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.GetErr != nil {
		return nil, f.GetErr
	}
	return &client.StateItem{Key: key, Value: slices.Clone(f.data[storeName][key])}, nil
}

// GetBulkState returns one item per key, with an empty value for absent keys.
func (f *FakeStateStore) GetBulkState(_ context.Context, storeName string, keys []string, _ map[string]string, _ int32) ([]*client.BulkStateItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bulkGets = append(f.bulkGets, len(keys))
	if f.BulkGetErr != nil {
		return nil, f.BulkGetErr
	}
	items := make([]*client.BulkStateItem, 0, len(keys))
	for _, k := range keys {
		items = append(items, &client.BulkStateItem{Key: k, Value: slices.Clone(f.data[storeName][k])})
	}
	return items, nil
}

// DeleteBulkState removes keys. Absent keys are ignored.
func (f *FakeStateStore) DeleteBulkState(_ context.Context, storeName string, keys []string, _ map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bulkDeletes = append(f.bulkDeletes, len(keys))
	if f.BulkDeleteErr != nil {
		return f.BulkDeleteErr
	}
	for _, k := range keys {
		delete(f.data[storeName], k)
		delete(f.meta, storeName+"/"+k)
	}
	return nil
}

// Delete removes one key without going through the error hooks, to simulate expiry.
func (f *FakeStateStore) Delete(storeName, key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.data[storeName], key)
}

// Keys returns the sorted keys held for storeName.
func (f *FakeStateStore) Keys(storeName string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Sorted(maps.Keys(f.data[storeName]))
}

// SaveMetadata returns the metadata passed with the last save of key.
func (f *FakeStateStore) SaveMetadata(storeName, key string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.meta[storeName+"/"+key]
	if !ok {
		return nil, fmt.Errorf("no save recorded for %q in %q", key, storeName)
	}
	return maps.Clone(m), nil
}

// SetGetErr sets GetErr under the fake's lock, for tests that change it while the fake is in use.
func (f *FakeStateStore) SetGetErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.GetErr = err
}

// BulkCalls returns the number of keys of every GetBulkState and DeleteBulkState call, in order.
func (f *FakeStateStore) BulkCalls() (gets, deletes []int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.bulkGets), slices.Clone(f.bulkDeletes)
}
