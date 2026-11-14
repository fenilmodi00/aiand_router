package cluster

import (
	"context"
	"os"
	"strconv"
	"sync"

	lru "github.com/hashicorp/golang-lru/v2"
)

// cpuGate serializes uncancellable embed runs and drops callers whose
// context expires before a run slot is free. Identical texts share one
// in-flight run and a small LRU of finished vectors.
type cpuGate struct {
	sem      chan struct{}
	cache    *lru.Cache[string, []float32]
	mu       sync.Mutex
	inflight map[string]*gateCall
}

type gateCall struct {
	done chan struct{}
	vec  []float32
	err  error
}

func newCPUGate() (*cpuGate, error) {
	n := envClamp("ROUTER_EMBED_CACHE_ENTRIES", 1024, 1, 8192)
	cache, err := lru.New[string, []float32](n)
	if err != nil {
		return nil, err
	}
	return &cpuGate{
		sem:      make(chan struct{}, 1),
		cache:    cache,
		inflight: make(map[string]*gateCall),
	}, nil
}

func (g *cpuGate) lock()   { g.mu.Lock() }
func (g *cpuGate) unlock() { g.mu.Unlock() }

// Do runs fn at most once per key while a leader is in flight. A caller
// whose ctx is already done, or that loses the race for the single run
// slot, returns ctx.Err() and does not start fn.
func (g *cpuGate) Do(ctx context.Context, key string, fn func() ([]float32, error)) ([]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if vec, ok := g.cache.Get(key); ok {
		return copyVec(vec), nil
	}

	g.lock()
	if call, ok := g.inflight[key]; ok {
		g.unlock()
		select {
		case <-call.done:
			if call.err != nil {
				return nil, call.err
			}
			return copyVec(call.vec), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	call := &gateCall{done: make(chan struct{})}
	g.inflight[key] = call
	g.unlock()

	defer func() {
		g.lock()
		delete(g.inflight, key)
		g.unlock()
		close(call.done)
	}()

	select {
	case g.sem <- struct{}{}:
	case <-ctx.Done():
		call.err = ctx.Err()
		return nil, ctx.Err()
	}
	defer func() { <-g.sem }()

	if vec, ok := g.cache.Get(key); ok {
		call.vec = copyVec(vec)
		return copyVec(call.vec), nil
	}
	vec, err := fn()
	if err != nil {
		call.err = err
		return nil, err
	}
	stored := copyVec(vec)
	g.cache.Add(key, stored)
	call.vec = stored
	return copyVec(stored), nil
}

func copyVec(v []float32) []float32 {
	if v == nil {
		return nil
	}
	out := make([]float32, len(v))
	copy(out, v)
	return out
}

func envClamp(name string, def, lo, hi int) int {
	raw := os.Getenv(name)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}
