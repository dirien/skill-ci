package reportstore

import (
	"container/list"
	"sync"
)

// cache is a byte-bounded LRU for content-addressed blobs. Entries are keyed
// by digest, so they never go stale. Callers must treat returned slices as
// read-only.
type cache struct {
	mu      sync.Mutex
	limit   int64
	size    int64
	order   *list.List // front is the most recently used
	entries map[string]*list.Element
}

type entry struct {
	key  string
	data []byte
}

func newCache(limit int64) *cache {
	return &cache{limit: limit, order: list.New(), entries: map[string]*list.Element{}}
}

// get returns the cached bytes for key and marks them recently used.
func (c *cache) get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(el)
	e, _ := el.Value.(*entry) // only *entry is stored
	return e.data, true
}

// put stores data under key and evicts the least recently used entries until
// the cache fits its limit. Data larger than the whole cache is not stored.
func (c *cache) put(key string, data []byte) {
	size := int64(len(data))
	if size > c.limit {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		c.order.MoveToFront(el)
		return
	}
	c.entries[key] = c.order.PushFront(&entry{key: key, data: data})
	c.size += size
	for c.size > c.limit {
		oldest := c.order.Back()
		e, _ := oldest.Value.(*entry) // only *entry is stored
		c.order.Remove(oldest)
		delete(c.entries, e.key)
		c.size -= int64(len(e.data))
	}
}
