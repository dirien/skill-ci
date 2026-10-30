package reportstore

import "testing"

func TestCacheEvictsLeastRecentlyUsed(t *testing.T) {
	c := newCache(10)
	c.put("a", []byte("12345"))
	c.put("b", []byte("12345"))
	if _, ok := c.get("a"); !ok { // a is now newer than b
		t.Fatal("a missing")
	}
	c.put("c", []byte("12345")) // 15 bytes: b is the oldest and goes
	tests := []struct {
		key  string
		want bool
	}{{"a", true}, {"b", false}, {"c", true}}
	for _, tt := range tests {
		if _, ok := c.get(tt.key); ok != tt.want {
			t.Errorf("get(%q) present = %v, want %v", tt.key, ok, tt.want)
		}
	}
	if c.size != 10 {
		t.Errorf("size = %d, want 10", c.size)
	}
}

func TestCacheSkipsOversizedAndDuplicateEntries(t *testing.T) {
	c := newCache(4)
	c.put("big", []byte("12345"))
	if _, ok := c.get("big"); ok {
		t.Error("entry larger than the cache was stored")
	}
	c.put("k", []byte("12"))
	c.put("k", []byte("12"))
	if c.size != 2 || c.order.Len() != 1 {
		t.Errorf("size = %d, entries = %d, want 2 and 1", c.size, c.order.Len())
	}
}
