package app

import (
	"container/list"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var errPageBusy = errors.New("page work queue is full")

type PageValue struct {
	Bytes []byte
	MIME  string
	Still bool
}
type memoryItem struct {
	key   string
	value PageValue
}
type diskItem struct {
	key, path, mime string
	size            int64
	still           bool
}
type cacheFlight struct {
	done     chan struct{}
	cancel   context.CancelFunc
	waiters  int
	value    PageValue
	err      error
	priority prioritySignal
}
type PageCache struct {
	mu                                                        sync.Mutex
	memory                                                    map[string]*list.Element
	disk                                                      map[string]*list.Element
	ml, dl                                                    *list.List
	memoryBytes, diskBytes, memoryLimit, diskLimit, itemLimit int64
	generation                                                string
	diskReady                                                 bool
	closed                                                    bool
	flights                                                   map[string]*cacheFlight
	ctx                                                       context.Context
	cancel                                                    context.CancelFunc
	wg                                                        sync.WaitGroup
	hits, misses                                              uint64
	directoryLock                                             *os.File
}

func NewPageCache(base string, memory, disk, item int64) (*PageCache, error) {
	if e := os.MkdirAll(base, 0700); e != nil {
		return nil, e
	}
	directoryLock, e := LockDirectory(base)
	if e != nil {
		return nil, e
	}
	generation, e := os.MkdirTemp(base, "gen-")
	if e != nil {
		directoryLock.Close()
		return nil, e
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &PageCache{memory: map[string]*list.Element{}, disk: map[string]*list.Element{}, ml: list.New(), dl: list.New(), memoryLimit: memory, diskLimit: disk, itemLimit: item, generation: generation, flights: map[string]*cacheFlight{}, ctx: ctx, cancel: cancel}
	c.directoryLock = directoryLock
	c.wg.Add(1)
	go func() { defer c.wg.Done(); c.reclaim(base) }()
	return c, nil
}
func (c *PageCache) reclaim(base string) {
	f, e := os.Open(base)
	if e != nil {
		return
	}
	defer f.Close()
	ticker := time.NewTicker(2 * time.Millisecond)
	defer ticker.Stop()
	for {
		dirs, e := f.ReadDir(64)
		for _, dir := range dirs {
			p := filepath.Join(base, dir.Name())
			if p == c.generation || !strings.HasPrefix(dir.Name(), "gen-") || !dir.IsDir() {
				continue
			}
			d, e := os.Open(p)
			if e != nil {
				return
			}
			for {
				entries, re := d.ReadDir(64)
				for _, entry := range entries {
					select {
					case <-c.ctx.Done():
						d.Close()
						return
					case <-ticker.C:
					}
					if entry.IsDir() {
						d.Close()
						return
					}
					if e = os.Remove(filepath.Join(p, entry.Name())); e != nil && !errors.Is(e, os.ErrNotExist) {
						d.Close()
						return
					}
				}
				if re == io.EOF {
					break
				}
				if re != nil {
					d.Close()
					return
				}
			}
			d.Close()
			if e = os.Remove(p); e != nil && !errors.Is(e, os.ErrNotExist) {
				return
			}
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return
		}
	}
	c.mu.Lock()
	c.diskReady = true
	c.mu.Unlock()
}
func (c *PageCache) Load(ctx context.Context, key string, diskAdmission bool, fn func(context.Context) (PageValue, error)) (PageValue, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return PageValue{}, errors.New("page cache closed")
	}
	if el := c.memory[key]; el != nil {
		c.ml.MoveToFront(el)
		v := el.Value.(memoryItem).value
		c.hits++
		c.mu.Unlock()
		return v, nil
	}
	if el := c.disk[key]; el != nil {
		di := el.Value.(diskItem)
		c.dl.MoveToFront(el)
		c.mu.Unlock()
		f, e := os.Open(di.path)
		var b []byte
		if e == nil {
			b, e = io.ReadAll(io.LimitReader(f, c.itemLimit+1))
			f.Close()
		}
		c.mu.Lock()
		if !c.closed && c.disk[key] == el {
			if e == nil && int64(len(b)) == di.size {
				v := PageValue{Bytes: b, MIME: di.mime, Still: di.still}
				c.hits++
				c.admitMemory(key, v)
				c.mu.Unlock()
				return v, nil
			}
			c.removeDisk(el)
			c.mu.Unlock()
			c.removeDiskFile(di.path)
		} else {
			c.mu.Unlock()
		}
		return c.Load(ctx, key, diskAdmission, fn)
	}
	flight := c.flights[key]
	if flight == nil {
		if len(c.flights) >= 128 {
			c.mu.Unlock()
			return PageValue{}, errPageBusy
		}
		shared, cancel := context.WithCancel(c.ctx)
		flight = &cacheFlight{done: make(chan struct{}), cancel: cancel}
		flight.priority.parent = signalFor(ctx)
		shared = context.WithValue(shared, priorityKey{}, &flight.priority)
		shared = context.WithValue(shared, backgroundKey{}, background(ctx))
		c.flights[key] = flight
		c.misses++
		c.wg.Add(1)
		go func(f *cacheFlight) {
			defer c.wg.Done()
			value, e := fn(shared)
			if e == nil && int64(len(value.Bytes)) > c.itemLimit {
				e = errors.New("page exceeds configured limit")
			}
			c.mu.Lock()
			f.value = value
			f.err = e
			if e == nil && !c.closed && c.flights[key] == f {
				c.admitMemory(key, value)
			}
			c.mu.Unlock()
			if e == nil && diskAdmission {
				c.admitDisk(key, value, f)
			}
			c.mu.Lock()
			if c.flights[key] == f {
				delete(c.flights, key)
			}
			close(f.done)
			c.mu.Unlock()
			cancel()
		}(flight)
	}
	if !background(ctx) || promoted(ctx) {
		flight.priority.foreground.Store(true)
	}
	flight.waiters++
	c.mu.Unlock()
	select {
	case <-ctx.Done():
		c.mu.Lock()
		flight.waiters--
		if flight.waiters == 0 {
			if c.flights[key] == flight {
				delete(c.flights, key)
			}
			flight.cancel()
		}
		c.mu.Unlock()
		return PageValue{}, ctx.Err()
	case <-flight.done:
		return flight.value, flight.err
	}
}
func (c *PageCache) admitMemory(key string, v PageValue) {
	size := int64(len(v.Bytes))
	if size > c.memoryLimit/4 || size == 0 {
		return
	}
	if el := c.memory[key]; el != nil {
		c.memoryBytes -= int64(len(el.Value.(memoryItem).value.Bytes))
		c.ml.Remove(el)
	}
	for c.memoryBytes+size > c.memoryLimit || c.ml.Len() >= 2048 {
		el := c.ml.Back()
		if el == nil {
			break
		}
		old := el.Value.(memoryItem)
		c.memoryBytes -= int64(len(old.value.Bytes))
		delete(c.memory, old.key)
		c.ml.Remove(el)
	}
	c.memory[key] = c.ml.PushFront(memoryItem{key, v})
	c.memoryBytes += size
}
func (c *PageCache) admitDisk(key string, v PageValue, flight *cacheFlight) {
	size := int64(len(v.Bytes))
	c.mu.Lock()
	ready := c.diskReady && !c.closed && c.flights[key] == flight
	c.mu.Unlock()
	if !ready || size > c.diskLimit/4 || size == 0 {
		return
	}
	tmp, e := os.CreateTemp(c.generation, ".write-")
	if e != nil {
		return
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	// Each admission owns its path so stale I/O cannot remove a replacement.
	p := tmpPath + ".page"
	_, e = tmp.Write(v.Bytes)
	if ce := tmp.Close(); e == nil {
		e = ce
	}
	if e == nil {
		e = os.Rename(tmpPath, p)
	}
	if e != nil {
		return
	}
	c.mu.Lock()
	if !c.diskReady || c.closed || c.flights[key] != flight {
		c.mu.Unlock()
		c.removeDiskFile(p)
		return
	}
	var removed []string
	if el := c.disk[key]; el != nil {
		removed = append(removed, el.Value.(diskItem).path)
		c.removeDisk(el)
	}
	for c.diskBytes+size > c.diskLimit || c.dl.Len() >= 8192 {
		el := c.dl.Back()
		if el == nil {
			break
		}
		removed = append(removed, el.Value.(diskItem).path)
		c.removeDisk(el)
	}
	c.disk[key] = c.dl.PushFront(diskItem{key, p, v.MIME, size, v.Still})
	c.diskBytes += size
	c.mu.Unlock()
	for _, path := range removed {
		c.removeDiskFile(path)
	}
}
func (c *PageCache) removeDisk(el *list.Element) {
	di := el.Value.(diskItem)
	c.diskBytes -= di.size
	delete(c.disk, di.key)
	c.dl.Remove(el)
}
func (c *PageCache) removeDiskFile(path string) {
	if e := os.Remove(path); e != nil && !errors.Is(e, os.ErrNotExist) {
		c.mu.Lock()
		c.diskReady = false
		c.mu.Unlock()
	}
}
func (c *PageCache) Stats() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return map[string]any{"memory_bytes": c.memoryBytes, "memory_limit": c.memoryLimit, "disk_bytes": c.diskBytes, "disk_limit": c.diskLimit, "disk_admission_ready": c.diskReady, "inflight": len(c.flights), "hits": c.hits, "misses": c.misses}
}
func (c *PageCache) Close() {
	c.mu.Lock()
	c.closed = true
	c.cancel()
	c.mu.Unlock()
	c.wg.Wait()
	_ = c.directoryLock.Close()
}
func (c *PageCache) Pressure() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	pressure := 0.0
	if c.memoryLimit > 0 {
		pressure = float64(c.memoryBytes) / float64(c.memoryLimit)
	}
	if c.diskLimit > 0 {
		pressure = max(pressure, float64(c.diskBytes)/float64(c.diskLimit))
	}
	return pressure
}
