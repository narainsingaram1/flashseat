package httpapi

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flashseat/flashseat/apps/api/internal/store"
	"github.com/flashseat/flashseat/apps/api/internal/venue"
)

// metaTTL bounds how stale an API replica's view of an event's configuration
// can be. Simulation runs wait longer than this between reconfiguring an event
// and sending traffic.
const metaTTL = time.Second

type metaSnapshot struct {
	meta    store.EventMeta
	err     error
	fetched time.Time
}

type metaEntry struct {
	refresh sync.Mutex
	current atomic.Pointer[metaSnapshot]
}

// eventCache serves event configuration and venue layouts from memory. One
// goroutine per event refreshes stale entries while others keep serving the
// previous value, so a refresh never stalls the hot path.
type eventCache struct {
	store  *store.Store
	mu     sync.Mutex
	load   sync.Mutex // one venue load at a time, so a new version is read once
	metas  map[string]*metaEntry
	venues map[string]*venue.Venue
}

func newEventCache(s *store.Store) *eventCache {
	return &eventCache{store: s, metas: map[string]*metaEntry{}, venues: map[string]*venue.Venue{}}
}

// refreshEvery is how often known events are re-read in the background. It
// bounds how stale a replica's view can be; the control plane waits longer
// than this after reconfiguring an event before sending traffic.
const refreshEvery = 500 * time.Millisecond

// staleLimit is the oldest value served without a synchronous re-read, in
// case the background refresher is failing.
const staleLimit = 3 * time.Second

func (c *eventCache) meta(ctx context.Context, eventID string) (store.EventMeta, error) {
	c.mu.Lock()
	e, ok := c.metas[eventID]
	if !ok {
		e = &metaEntry{}
		c.metas[eventID] = e
	}
	c.mu.Unlock()
	if cur := e.current.Load(); cur != nil && cur.err == nil && time.Since(cur.fetched) < staleLimit {
		return cur.meta, nil
	}
	return c.refresh(ctx, eventID, e)
}

func (c *eventCache) refresh(ctx context.Context, eventID string, e *metaEntry) (store.EventMeta, error) {
	e.refresh.Lock()
	defer e.refresh.Unlock()
	if cur := e.current.Load(); cur != nil && cur.err == nil && time.Since(cur.fetched) < refreshEvery/2 {
		return cur.meta, nil
	}
	m, err := c.store.EventMeta(ctx, eventID)
	if err != nil {
		return m, err
	}
	prev := e.current.Load()
	e.current.Store(&metaSnapshot{meta: m, fetched: time.Now()})
	if prev == nil || prev.meta.Version != m.Version {
		// A new inventory version means a new venue: load it now, before
		// the first buyer needs it.
		go func() { _, _ = c.venue(context.Background(), m) }()
	}
	return m, nil
}

// run keeps every event this replica has seen fresh until ctx ends.
func (c *eventCache) run(ctx context.Context) {
	t := time.NewTicker(refreshEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		c.mu.Lock()
		entries := make(map[string]*metaEntry, len(c.metas))
		for id, e := range c.metas {
			entries[id] = e
		}
		c.mu.Unlock()
		for id, e := range entries {
			rctx, cancel := context.WithTimeout(ctx, time.Second)
			_, _ = c.refresh(rctx, id, e)
			cancel()
		}
	}
}

func (c *eventCache) venue(ctx context.Context, m store.EventMeta) (*venue.Venue, error) {
	key := fmt.Sprintf("%s:%d", m.ID, m.Version)
	c.mu.Lock()
	v, ok := c.venues[key]
	c.mu.Unlock()
	if ok {
		return v, nil
	}
	c.load.Lock()
	defer c.load.Unlock()
	c.mu.Lock()
	v, ok = c.venues[key]
	c.mu.Unlock()
	if ok {
		return v, nil
	}
	layout, err := c.store.Layout(ctx, m.ID)
	if err != nil {
		return nil, err
	}
	v, err = venue.Build(layout)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	for k := range c.venues {
		if len(k) > len(m.ID) && k[:len(m.ID)+1] == m.ID+":" {
			delete(c.venues, k)
		}
	}
	c.venues[key] = v
	c.mu.Unlock()
	return v, nil
}

// forget drops an event's cached configuration so the next read is fresh.
func (c *eventCache) forget(eventID string) {
	c.mu.Lock()
	e, ok := c.metas[eventID]
	c.mu.Unlock()
	if ok {
		if cur := e.current.Load(); cur != nil {
			stale := *cur
			stale.fetched = time.Time{}
			e.current.Store(&stale)
		}
	}
}
