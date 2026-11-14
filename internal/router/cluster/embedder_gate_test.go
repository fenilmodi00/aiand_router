package cluster

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCPUGate_CancelledCallerDoesNotStartSecondRun(t *testing.T) {
	g, err := newCPUGate()
	require.NoError(t, err)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	go func() {
		_, _ = g.Do(context.Background(), "a", func() ([]float32, error) {
			calls.Add(1)
			close(started)
			<-release
			return []float32{1}, nil
		})
	}()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = g.Do(ctx, "b", func() ([]float32, error) {
		calls.Add(1)
		return []float32{2}, nil
	})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, int32(1), calls.Load())
	close(release)
}

func TestCPUGate_SingleflightSameKey(t *testing.T) {
	g, err := newCPUGate()
	require.NoError(t, err)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	var once sync.Once
	fn := func() ([]float32, error) {
		calls.Add(1)
		once.Do(func() { close(started) })
		<-release
		return []float32{7, 8}, nil
	}
	type result struct {
		vec []float32
		err error
	}
	out := make(chan result, 2)
	for range 2 {
		go func() {
			vec, err := g.Do(context.Background(), "same", fn)
			out <- result{vec, err}
		}()
	}
	<-started
	close(release)
	for range 2 {
		r := <-out
		require.NoError(t, r.err)
		require.Equal(t, []float32{7, 8}, r.vec)
	}
	require.Equal(t, int32(1), calls.Load())
}

func TestCPUGate_CacheHitSkipsRun(t *testing.T) {
	g, err := newCPUGate()
	require.NoError(t, err)
	var calls atomic.Int32
	fn := func() ([]float32, error) {
		calls.Add(1)
		return []float32{3}, nil
	}
	vec, err := g.Do(context.Background(), "k", fn)
	require.NoError(t, err)
	require.Equal(t, []float32{3}, vec)
	vec, err = g.Do(context.Background(), "k", fn)
	require.NoError(t, err)
	require.Equal(t, []float32{3}, vec)
	require.Equal(t, int32(1), calls.Load())
}

func TestCPUGate_DistinctKeysDoNotShareCache(t *testing.T) {
	g, err := newCPUGate()
	require.NoError(t, err)
	var calls atomic.Int32
	fn := func(v float32) func() ([]float32, error) {
		return func() ([]float32, error) {
			calls.Add(1)
			return []float32{v}, nil
		}
	}
	a, err := g.Do(context.Background(), "a", fn(1))
	require.NoError(t, err)
	b, err := g.Do(context.Background(), "b", fn(2))
	require.NoError(t, err)
	require.Equal(t, []float32{1}, a)
	require.Equal(t, []float32{2}, b)
	require.Equal(t, int32(2), calls.Load())
}
