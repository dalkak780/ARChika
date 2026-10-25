package app

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCacheCoalescesReadersAndEnforcesByteQuotas(t *testing.T) {
	c, e := NewPageCache(t.TempDir(), 64<<10, 128<<10, 16<<10)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	var calls atomic.Int32
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := c.Load(context.Background(), "shared", false, func(context.Context) (PageValue, error) {
				calls.Add(1)
				<-start
				return PageValue{Bytes: []byte("shared original bytes"), MIME: "image/png"}, nil
			})
			if e != nil || string(v.Bytes) != "shared original bytes" {
				t.Errorf("coalesced page corrupted: %v", e)
			}
		}()
	}
	deadline := time.Now().Add(time.Second)
	for {
		c.mu.Lock()
		f := c.flights["shared"]
		waiting := 0
		if f != nil {
			waiting = f.waiters
		}
		c.mu.Unlock()
		if waiting == 12 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("readers did not join flight")
		}
		time.Sleep(time.Millisecond)
	}
	close(start)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("decoded same key %d times", calls.Load())
	}
	for i := range 80 {
		_, e = c.Load(context.Background(), fmt.Sprint(i), true, func(context.Context) (PageValue, error) {
			return PageValue{Bytes: bytes.Repeat([]byte{'x'}, 8<<10), MIME: "image/png"}, nil
		})
		if e != nil {
			t.Fatal(e)
		}
	}
	stats := c.Stats()
	if stats["memory_bytes"].(int64) > 64<<10 || stats["disk_bytes"].(int64) > 128<<10 {
		t.Fatalf("cache exceeded byte quotas %+v", stats)
	}
}
func TestOneCancelledWaiterDoesNotCancelSharedPage(t *testing.T) {
	c, e := NewPageCache(t.TempDir(), 1024, 0, 1024)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	release := make(chan struct{})
	fn := func(ctx context.Context) (PageValue, error) {
		close(started)
		select {
		case <-release:
			return PageValue{Bytes: []byte("page")}, nil
		case <-ctx.Done():
			return PageValue{}, ctx.Err()
		}
	}
	first := make(chan error, 1)
	go func() { _, e := c.Load(ctx, "page", false, fn); first <- e }()
	<-started
	second := make(chan error, 1)
	go func() {
		v, e := c.Load(context.Background(), "page", false, fn)
		if e == nil && string(v.Bytes) != "page" {
			e = fmt.Errorf("wrong shared bytes")
		}
		second <- e
	}()
	deadline := time.Now().Add(time.Second)
	for {
		c.mu.Lock()
		waiting := c.flights["page"].waiters
		c.mu.Unlock()
		if waiting == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("second waiter did not join")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if e = <-first; e != context.Canceled {
		t.Fatal("cancelled reader did not return cancellation")
	}
	close(release)
	if e = <-second; e != nil {
		t.Fatal("one cancellation aborted another reader", e)
	}
}
