package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"testing/synctest"
	"time"
)

func TestCancelledPageFlightDoesNotAffectReplacement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, e := NewPageCache(t.TempDir(), 1024, 0, 1024)
		if e != nil {
			t.Fatal(e)
		}
		defer c.Close()
		load := func(ctx context.Context, fn func(context.Context) (PageValue, error)) <-chan error {
			result := make(chan error, 1)
			go func() {
				v, e := c.Load(ctx, "page", false, fn)
				if e == nil && string(v.Bytes) != "page" {
					e = errors.New("wrong shared bytes")
				}
				result <- e
			}()
			return result
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		releaseFirst := make(chan struct{})
		finishFirst := sync.OnceFunc(func() { close(releaseFirst) })
		defer finishFirst()
		first := load(ctx, func(ctx context.Context) (PageValue, error) {
			<-releaseFirst
			return PageValue{}, ctx.Err()
		})
		synctest.Wait()
		cancel()
		if e = <-first; e != context.Canceled {
			t.Fatalf("cancelled reader returned %v", e)
		}
		releaseReplacement := make(chan struct{})
		finishReplacement := sync.OnceFunc(func() { close(releaseReplacement) })
		defer finishReplacement()
		replacementStarted := false
		second := load(context.Background(), func(context.Context) (PageValue, error) {
			replacementStarted = true
			<-releaseReplacement
			return PageValue{Bytes: []byte("page")}, nil
		})
		synctest.Wait()
		if !replacementStarted {
			t.Fatal("new reader joined the cancelled flight")
		}
		finishFirst()
		synctest.Wait()
		third := load(context.Background(), func(context.Context) (PageValue, error) {
			return PageValue{}, errors.New("replacement flight was removed while still running")
		})
		synctest.Wait()
		finishReplacement()
		if e = <-second; e != nil {
			t.Fatal("replacement reader failed", e)
		}
		if e = <-third; e != nil {
			t.Fatal("reader did not share the replacement flight", e)
		}
	})
}

func TestDiskReadDoesNotBlockReplacement(t *testing.T) {
	c, e := NewPageCache(t.TempDir(), 0, 1024, 256)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	c.wg.Wait()
	path := filepath.Join(c.generation, "blocked")
	if e = syscall.Mkfifo(path, 0600); e != nil {
		t.Fatal(e)
	}
	c.mu.Lock()
	c.disk["page"] = c.dl.PushFront(diskItem{key: "page", path: path, size: 3})
	c.diskBytes = 3
	c.mu.Unlock()
	result := make(chan PageValue, 1)
	go func() {
		v, err := c.Load(context.Background(), "page", false, func(context.Context) (PageValue, error) {
			return PageValue{}, errors.New("replacement was lost")
		})
		if err != nil {
			t.Error(err)
		}
		result <- v
	}()
	writer, e := os.OpenFile(path, os.O_WRONLY, 0)
	if e != nil {
		t.Fatal(e)
	}
	defer writer.Close()
	replaced := make(chan struct{})
	go func() {
		flight := &cacheFlight{}
		c.mu.Lock()
		c.flights["page"] = flight
		c.mu.Unlock()
		c.admitDisk("page", PageValue{Bytes: []byte("new"), MIME: "image/png", Still: true}, flight)
		close(replaced)
	}()
	select {
	case <-replaced:
	case <-time.After(5 * time.Second):
		t.Fatal("disk read held the cache mutex")
	}
	if _, e = writer.Write([]byte("old")); e != nil {
		t.Fatal(e)
	}
	writer.Close()
	select {
	case v := <-result:
		if string(v.Bytes) != "new" || v.MIME != "image/png" || !v.Still {
			t.Fatalf("read stale entry: %+v", v)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("disk reader did not finish")
	}
	entries, e := os.ReadDir(c.generation)
	if e != nil {
		t.Fatal(e)
	}
	if len(entries) != 1 {
		t.Fatalf("left stale cache files: %v", entries)
	}
	if got := c.Stats()["disk_bytes"]; got != int64(3) {
		t.Fatalf("disk bytes = %v", got)
	}
}
