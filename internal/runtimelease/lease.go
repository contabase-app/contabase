// Package runtimelease carries an optional early-release callback for a
// server runtime read lease. Handlers that finish their database work before a
// long streaming response can release the lease immediately, allowing a
// restore to take the write lock while the response is still being sent.
package runtimelease

import (
	"context"
	"sync"
	"sync/atomic"
)

type releaseKey struct{}
type generationKey struct{}

type lease struct {
	release func()
	held    atomic.Bool
}

// WithRelease shares ownership state across derived request contexts.
func WithRelease(ctx context.Context, release func()) context.Context {
	if ctx == nil || release == nil {
		return ctx
	}
	l := &lease{}
	l.held.Store(true)
	l.release = sync.OnceFunc(func() {
		l.held.Store(false)
		release()
	})
	return context.WithValue(ctx, releaseKey{}, l)
}

func Held(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	l, ok := ctx.Value(releaseKey{}).(*lease)
	return ok && l.held.Load()
}

func WithGeneration(ctx context.Context, generation string) context.Context {
	return context.WithValue(ctx, generationKey{}, generation)
}

func Generation(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	generation, _ := ctx.Value(generationKey{}).(string)
	return generation
}

// Release releases the runtime lease carried by ctx, if any. It is safe to
// call on requests that never acquired a lease.
func Release(ctx context.Context) {
	if ctx == nil {
		return
	}
	l, ok := ctx.Value(releaseKey{}).(*lease)
	if !ok || l == nil {
		return
	}
	l.release()
}
