package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	ar "archika/internal/archive"
)

type decoderRequest struct {
	Op      string    `json:"op"`
	Path    string    `json:"path"`
	Kind    string    `json:"kind"`
	Limits  ar.Limits `json:"limits"`
	Entry   ar.Entry  `json:"entry"`
	Locator int       `json:"locator"`
}
type decoderResponse struct {
	Entries      []ar.Entry      `json:"entries,omitempty"`
	Locators     []int           `json:"locators,omitempty"`
	Capabilities ar.Capabilities `json:"capabilities"`
	Code         string          `json:"code,omitempty"`
	Message      string          `json:"message,omitempty"`
	Size         int64           `json:"size,omitempty"`
	Error        string          `json:"error,omitempty"`
}

// DecoderMain runs only in an exec'd helper, never on the HTTP-serving process.
// Its root descriptor is inherited, so neither a symlink swap nor a path string
// can redirect decoder I/O outside the descriptor's library root.
func DecoderMain() error {
	if e := limitHelper(); e != nil {
		return e
	}
	root, e := os.OpenRoot(descriptorPath())
	if e != nil {
		return e
	}
	defer root.Close()
	var engine ar.Adapter
	defer func() {
		if engine != nil {
			engine.Close()
		}
	}()
	reader := bufio.NewReaderSize(os.Stdin, 64<<10)
	for {
		line, e := readLine(reader, 64<<10)
		if e == io.EOF {
			return nil
		}
		if e != nil {
			return e
		}
		var req decoderRequest
		if e = json.Unmarshal(line, &req); e != nil {
			return e
		}
		response := decoderResponse{}
		var bytes []byte
		switch req.Op {
		case "init":
			if engine != nil {
				return errors.New("decoder already initialized")
			}
			engine, e = ar.New(root, req.Path, req.Kind, req.Limits)
		case "list":
			if engine == nil {
				return errors.New("decoder not initialized")
			}
			response.Entries, response.Capabilities, e = engine.List(context.Background())
			if e == nil {
				response.Locators = make([]int, len(response.Entries))
				for i, entry := range response.Entries {
					response.Locators[i] = entry.Locator
				}
			}
		case "open":
			if engine == nil {
				return errors.New("decoder not initialized")
			}
			req.Entry.Locator = req.Locator
			var stream io.ReadCloser
			stream, e = engine.Open(context.Background(), req.Entry)
			if e == nil {
				bytes, e = boundedRead(context.Background(), stream, req.Limits.MaxEntryBytes)
				ce := stream.Close()
				if e == nil {
					e = ce
				}
			}
			response.Size = int64(len(bytes))
		default:
			return errors.New("invalid decoder operation")
		}
		if e != nil {
			response.Code = ar.Code(e)
			response.Message = ar.Message(e)
			response.Error = "decoder operation failed"
			response.Size = 0
			bytes = nil
		}
		if e = json.NewEncoder(os.Stdout).Encode(response); e != nil {
			return e
		}
		if len(bytes) > 0 {
			if _, e = os.Stdout.Write(bytes); e != nil {
				return e
			}
		}
	}
}
func readLine(r *bufio.Reader, limit int) ([]byte, error) {
	var out []byte
	for {
		line, more, e := r.ReadLine()
		if e != nil {
			return nil, e
		}
		if len(out)+len(line) > limit {
			return nil, errors.New("protocol payload limit")
		}
		out = append(out, line...)
		if !more {
			return out, nil
		}
	}
}

type remoteAdapter struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	out    *bufio.Reader
	limits ar.Limits
	dead   atomic.Bool
}

func newRemoteAdapter(ctx context.Context, root *os.Root, path, kind string, limits ar.Limits) (ar.Adapter, error) {
	exe, e := os.Executable()
	if e != nil {
		return nil, e
	}
	fd, e := root.Open(".")
	if e != nil {
		return nil, e
	}
	defer fd.Close()
	cmd := exec.Command(exe, "decoder")
	cmd.ExtraFiles = []*os.File{fd}
	cmd.Env = []string{"GOMEMLIMIT=256MiB", "GOMAXPROCS=2", "PATH=/usr/bin:/bin"}
	in, e := cmd.StdinPipe()
	if e != nil {
		return nil, e
	}
	stdout, e := cmd.StdoutPipe()
	if e != nil {
		return nil, e
	}
	cmd.Stderr = io.Discard
	if e = cmd.Start(); e != nil {
		return nil, e
	}
	r := &remoteAdapter{cmd: cmd, in: in, out: bufio.NewReaderSize(stdout, 64<<10), limits: limits}
	_, _, e = r.call(ctx, decoderRequest{Op: "init", Path: path, Kind: kind, Limits: limits})
	if e != nil {
		r.Close()
		return nil, e
	}
	return r, nil
}
func (r *remoteAdapter) call(ctx context.Context, req decoderRequest) (decoderResponse, []byte, error) {
	if r.dead.Load() {
		return decoderResponse{}, nil, errors.New("decoder process unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	type result struct {
		response decoderResponse
		bytes    []byte
		err      error
	}
	done := make(chan result, 1)
	go func() {
		var res result
		res.err = json.NewEncoder(r.in).Encode(req)
		if res.err == nil {
			var line []byte
			line, res.err = readLine(r.out, 32<<20)
			if res.err == nil {
				res.err = json.Unmarshal(line, &res.response)
			}
		}
		if res.err == nil && res.response.Size > 0 {
			if res.response.Size > r.limits.MaxEntryBytes {
				res.err = errors.New("decoder page limit")
			} else {
				res.bytes = make([]byte, res.response.Size)
				_, res.err = io.ReadFull(r.out, res.bytes)
			}
		}
		if res.err == nil && res.response.Error != "" {
			res.err = &remoteError{code: res.response.Code, message: res.response.Message}
		}
		done <- res
	}()
	select {
	case <-ctx.Done():
		helperLog(ctx).Warn("decoder process timed out or cancelled", "page", req.Entry.ID, "error", ctx.Err().Error())
		r.dead.Store(true)
		_ = r.cmd.Process.Kill()
		res := <-done
		_ = res
		return decoderResponse{}, nil, ctx.Err()
	case res := <-done:
		if res.err != nil {
			if _, ok := res.err.(*remoteError); !ok {
				helperLog(ctx).Warn("decoder process failed", "page", req.Entry.ID, "error", res.err.Error())
				r.dead.Store(true)
				_ = r.cmd.Process.Kill()
			}
		}
		return res.response, res.bytes, res.err
	}
}

type remoteError struct{ code, message string }

func (e *remoteError) Error() string { return "archive decoder: " + e.code }
func (r *remoteAdapter) List(ctx context.Context) ([]ar.Entry, ar.Capabilities, error) {
	res, _, e := r.call(ctx, decoderRequest{Op: "list", Limits: r.limits})
	if e == nil {
		if len(res.Entries) != len(res.Locators) {
			return nil, res.Capabilities, errors.New("invalid decoder manifest locators")
		}
		for i := range res.Entries {
			res.Entries[i].Locator = res.Locators[i]
		}
	}
	return res.Entries, res.Capabilities, e
}
func (r *remoteAdapter) Open(ctx context.Context, entry ar.Entry) (io.ReadCloser, error) {
	_, b, e := r.call(ctx, decoderRequest{Op: "open", Entry: entry, Locator: entry.Locator, Limits: r.limits})
	if e != nil {
		return nil, e
	}
	return &sliceReader{b: b}, nil
}
func (r *remoteAdapter) Close() error {
	if r.cmd.Process != nil {
		_ = r.cmd.Process.Kill()
	}
	_ = r.in.Close()
	return r.cmd.Wait()
}

type sliceReader struct {
	b      []byte
	offset int
}

func (r *sliceReader) Read(p []byte) (int, error) {
	if r.offset >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.offset:])
	r.offset += n
	return n, nil
}
func (r *sliceReader) Close() error { r.b = nil; return nil }

type archiveSession struct {
	engine ar.Adapter
	root   *os.Root
	mu     sync.Mutex
	refs   int
	used   time.Time
	key    string
}
type ArchivePool struct {
	mu        sync.Mutex
	sessions  map[string]*archiveSession
	max       int
	pageBytes int64
	changed   chan struct{}
	closed    bool
}

func NewArchivePool(max int, pageBytes int64) *ArchivePool {
	return &ArchivePool{sessions: map[string]*archiveSession{}, max: max, pageBytes: pageBytes, changed: make(chan struct{})}
}
func (p *ArchivePool) With(ctx context.Context, al Alias, fn func(ar.Adapter) error) error {
	key := al.WorkID + ":" + strconv.FormatInt(al.Version, 10) + ":" + al.RootID + ":" + al.RootPath + ":" + al.Path
	for {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return errors.New("archive pool closed")
		}
		s := p.sessions[key]
		if s == nil {
			if len(p.sessions) >= p.max {
				var oldest *archiveSession
				for _, candidate := range p.sessions {
					if candidate.refs == 0 && (oldest == nil || candidate.used.Before(oldest.used)) {
						oldest = candidate
					}
				}
				if oldest == nil {
					ch := p.changed
					p.mu.Unlock()
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-ch:
					}
					continue
				}
				oldest.engine.Close()
				oldest.root.Close()
				delete(p.sessions, oldest.key)
			}
			root, e := os.OpenRoot(al.RootPath)
			if e != nil {
				p.mu.Unlock()
				return e
			}
			limits := ar.Limits{MaxEntryBytes: p.pageBytes, MaxEntries: 100000}
			var engine ar.Adapter
			if al.Kind != "directory" {
				engine, e = newRemoteAdapter(ctx, root, al.Path, al.Kind, limits)
			} else {
				engine, e = ar.New(root, al.Path, al.Kind, limits)
			}
			if e != nil {
				root.Close()
				p.mu.Unlock()
				return e
			}
			s = &archiveSession{engine: engine, root: root, key: key}
			p.sessions[key] = s
		}
		s.refs++
		p.mu.Unlock()
		s.mu.Lock()
		err := fn(s.engine)
		s.mu.Unlock()
		p.mu.Lock()
		s.refs--
		s.used = time.Now()
		if r, ok := s.engine.(*remoteAdapter); ok && r.dead.Load() && s.refs == 0 && !p.closed {
			s.engine.Close()
			s.root.Close()
			delete(p.sessions, s.key)
		}
		close(p.changed)
		p.changed = make(chan struct{})
		p.mu.Unlock()
		return err
	}
}
func (p *ArchivePool) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	sessions := p.sessions
	p.sessions = map[string]*archiveSession{}
	for _, s := range sessions {
		if r, ok := s.engine.(*remoteAdapter); ok {
			_ = r.cmd.Process.Kill()
		}
	}
	close(p.changed)
	p.changed = make(chan struct{})
	p.mu.Unlock()
	for _, s := range sessions {
		s.mu.Lock()
		_ = s.engine.Close()
		_ = s.root.Close()
		s.mu.Unlock()
	}
}
func decodeErrorCode(e error) string {
	var re *remoteError
	if errors.As(e, &re) {
		return re.code
	}
	return ar.Code(e)
}
func (p *ArchivePool) Stats() map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	active := 0
	for _, s := range p.sessions {
		if s.refs > 0 {
			active++
		}
	}
	return map[string]any{"sessions": len(p.sessions), "limit": p.max, "active": active, "max_helper_memory": fmt.Sprintf("%d MiB", helperMemory>>20)}
}
