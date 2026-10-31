package reportsite

import (
	"container/list"
	"sync"
)

// maxBundleRefs bounds the memo of report annotations; past it the memo starts over.
const maxBundleRefs = 4096

// bundleMemo remembers what the report referrer of a digest says about its
// trial bundle. A manifest never changes for its digest, so a successful
// answer is kept; errors are not.
type bundleMemo struct {
	mu   sync.Mutex
	refs map[string]bundleRef
}

func newBundleMemo() *bundleMemo { return &bundleMemo{refs: map[string]bundleRef{}} }

func (m *bundleMemo) get(reportDigest string) (bundleRef, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ref, ok := m.refs[reportDigest]
	return ref, ok
}

func (m *bundleMemo) put(reportDigest string, ref bundleRef) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.refs) >= maxBundleRefs {
		m.refs = map[string]bundleRef{}
	}
	m.refs[reportDigest] = ref
}

// byteCache is a least recently used cache of rendered responses, bounded by
// the bytes it holds. A response larger than a quarter of the limit is not kept.
type byteCache struct {
	mu    sync.Mutex
	limit int64
	size  int64
	order *list.List
	items map[string]*list.Element
}

type cached struct {
	key  string
	data []byte
}

func newByteCache(limit int64) *byteCache {
	return &byteCache{limit: limit, order: list.New(), items: map[string]*list.Element{}}
}

// get returns the cached bytes for key; they must not be modified.
func (c *byteCache) get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(el)
	return el.Value.(*cached).data, true
}

func (c *byteCache) put(key string, data []byte) {
	n := int64(len(data) + len(key))
	if n > c.limit/4 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		c.size -= int64(len(el.Value.(*cached).data) + len(key))
		c.order.Remove(el)
		delete(c.items, key)
	}
	for c.size+n > c.limit && c.order.Len() > 0 {
		last := c.order.Back()
		item := last.Value.(*cached)
		c.size -= int64(len(item.data) + len(item.key))
		c.order.Remove(last)
		delete(c.items, item.key)
	}
	c.items[key] = c.order.PushFront(&cached{key: key, data: data})
	c.size += n
}
