package cost

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// cacheEntry is one priced lookup persisted to disk.
type cacheEntry struct {
	USDPerHour float64   `json:"usd_per_hour"`
	Source     string    `json:"source"`
	Observed   time.Time `json:"observed"` // when the price was fetched (also the spot AsOf)
}

// diskCache is a tiny JSON file cache so repeated runs don't refetch prices
// every time. It is best-effort: any read/write error degrades to a miss rather
// than failing the run. Spot entries respect a shorter freshness than on-demand
// because spot moves; both honor the configured TTL ceiling.
type diskCache struct {
	dir string
	ttl time.Duration
	now time.Time
}

func newDiskCache(dir string, ttl time.Duration, now time.Time) *diskCache {
	return &diskCache{dir: dir, ttl: ttl, now: now}
}

// path returns the on-disk file for a cache key (sanitized).
func (c *diskCache) path(key string) string {
	safe := make([]byte, 0, len(key))
	for i := 0; i < len(key); i++ {
		ch := key[i]
		if ch == '/' || ch == ':' || ch == ' ' {
			ch = '_'
		}
		safe = append(safe, ch)
	}
	return filepath.Join(c.dir, "truce-price-"+string(safe)+".json")
}

// get returns a cached entry when present and within TTL.
func (c *diskCache) get(key string) (cacheEntry, bool) {
	if c.dir == "" {
		return cacheEntry{}, false
	}
	data, err := os.ReadFile(c.path(key))
	if err != nil {
		return cacheEntry{}, false
	}
	var e cacheEntry
	if err := json.Unmarshal(data, &e); err != nil {
		return cacheEntry{}, false
	}
	if c.now.Sub(e.Observed) > c.ttl {
		return cacheEntry{}, false // stale
	}
	return e, true
}

// put writes an entry, creating the cache dir on first use. Errors are ignored:
// the cache is an optimization, never a correctness dependency.
func (c *diskCache) put(key string, e cacheEntry) {
	if c.dir == "" {
		return
	}
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return
	}
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	_ = os.WriteFile(c.path(key), data, 0o644)
}
