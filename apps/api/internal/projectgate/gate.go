// Package projectgate coordinates in-process project operations with deletion.
package projectgate

import (
	"context"
	"sync"
)

type entry struct {
	token chan struct{}
	refs  int
}

// Gate serializes operations for the same project without holding a global
// mutex during work. Idle entries are removed, so arbitrary IDs cannot leak locks.
type Gate struct {
	mu      sync.Mutex
	entries map[string]*entry
}

func New() *Gate { return &Gate{entries: make(map[string]*entry)} }

func (g *Gate) Lock(ctx context.Context, id string) (func(), error) {
	g.mu.Lock()
	e := g.entries[id]
	if e == nil {
		e = &entry{token: make(chan struct{}, 1)}
		e.token <- struct{}{}
		g.entries[id] = e
	}
	e.refs++
	g.mu.Unlock()
	releaseRef := func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		e.refs--
		if e.refs == 0 {
			delete(g.entries, id)
		}
	}
	select {
	case <-ctx.Done():
		releaseRef()
		return nil, ctx.Err()
	case <-e.token:
	}
	if err := ctx.Err(); err != nil {
		e.token <- struct{}{}
		releaseRef()
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(func() { e.token <- struct{}{}; releaseRef() }) }, nil
}
