package runtimelease

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

func TestDerivedContextsShareIdempotentLeaseAndGeneration(t *testing.T) {
	var releases atomic.Int32
	ctx := WithGeneration(WithRelease(context.Background(), func() { releases.Add(1) }), "G1")
	derived := context.WithValue(ctx, struct{}{}, "request copy")
	if !Held(derived) || Generation(derived) != "G1" {
		t.Fatal("missing shared lease")
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); Release(derived) }()
	}
	wg.Wait()
	if Held(ctx) || Held(derived) || releases.Load() != 1 || Generation(ctx) != "G1" {
		t.Fatal("release lost ownership state or generation")
	}
	Release(context.Background())
	Release(nil)
}
