package cms

import (
	"context"
	"sync"
	"time"
)

// Resolver is what the cache and the middleware need; *Client implements it.
type Resolver interface {
	Resolve(ctx context.Context, host, path, locale string) (*Resolution, error)
}

const maxCached = 4096

// Cache caches Resolve answers per host, path and locale for a short TTL.
type Cache struct {
	next Resolver
	ttl  time.Duration
	now  func() time.Time

	mu sync.Mutex
	m  map[string]cached
}

type cached struct {
	r   *Resolution
	exp time.Time
}

// Cached wraps r so identical Resolve calls within ttl hit memory. Errors are
// not cached. A ttl <= 0 disables caching.
func Cached(r Resolver, ttl time.Duration) *Cache {
	return &Cache{next: r, ttl: ttl, now: time.Now, m: map[string]cached{}}
}

// Resolve returns the cached answer or asks the wrapped resolver. The returned
// Resolution is shared; treat it as read-only.
func (c *Cache) Resolve(ctx context.Context, host, path, locale string) (*Resolution, error) {
	if c.ttl <= 0 {
		return c.next.Resolve(ctx, host, path, locale)
	}
	key := host + "\x00" + path + "\x00" + locale
	now := c.now()
	c.mu.Lock()
	e, ok := c.m[key]
	c.mu.Unlock()
	if ok && now.Before(e.exp) {
		return e.r, nil
	}
	r, err := c.next.Resolve(ctx, host, path, locale)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if len(c.m) >= maxCached {
		for k, v := range c.m {
			if !now.Before(v.exp) {
				delete(c.m, k)
			}
		}
		if len(c.m) >= maxCached {
			c.m = map[string]cached{}
		}
	}
	c.m[key] = cached{r, now.Add(c.ttl)}
	c.mu.Unlock()
	return r, nil
}

// Purge drops every cached answer, e.g. after the CMS announces a change.
func (c *Cache) Purge() {
	c.mu.Lock()
	c.m = map[string]cached{}
	c.mu.Unlock()
}
