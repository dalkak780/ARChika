package app

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"
)

func (a *App) cancelPrefetch(user string) {
	a.prefetchMu.Lock()
	defer a.prefetchMu.Unlock()
	for k, cancel := range a.prefetch {
		if user == "" || strings.HasPrefix(k, user+"/") {
			cancel()
			delete(a.prefetch, k)
		}
	}
}
func (a *App) cancelWorkPrefetch(work string) {
	a.prefetchMu.Lock()
	defer a.prefetchMu.Unlock()
	for k, cancel := range a.prefetch {
		if strings.HasSuffix(k, "/"+work) {
			cancel()
			delete(a.prefetch, k)
		}
	}
}
func (a *App) prefetchHTTP(w http.ResponseWriter, r *http.Request, u User) {
	wid := r.PathValue("id")
	al, e := a.AuthorizedAlias(r.Context(), u, wid)
	if e != nil {
		apiError(w, 404, "not_found", "Work unavailable")
		return
	}
	var in struct {
		ReaderID   string  `json:"reader_id"`
		Page       int     `json:"page"`
		Direction  int     `json:"direction"`
		Velocity   float64 `json:"velocity"`
		Visible    int     `json:"visible"`
		ByteBudget int64   `json:"byte_budget"`
		Pressure   float64 `json:"pressure"`
	}
	if r.Method == "DELETE" {
		in.ReaderID = r.URL.Query().Get("reader_id")
	} else if decodeJSON(w, r, &in) != nil {
		apiError(w, 400, "invalid_request", "Invalid prefetch request")
		return
	}
	if len(in.ReaderID) < 1 || len(in.ReaderID) > 80 || strings.ContainsAny(in.ReaderID, "/\\") {
		apiError(w, 400, "invalid_reader", "Invalid reader identity")
		return
	}
	key := u.ID + "/" + in.ReaderID + "/" + wid
	a.prefetchMu.Lock()
	if old := a.prefetch[key]; old != nil {
		old()
		delete(a.prefetch, key)
	}
	a.prefetchMu.Unlock()
	if r.Method == "DELETE" {
		jsonResponse(w, 200, map[string]bool{"ok": true})
		return
	}
	if in.Page < 0 || (in.Direction != 1 && in.Direction != -1) || in.Visible < 1 || in.Visible > 2 || in.Velocity < 0 || in.Velocity > 100 || math.IsNaN(in.Velocity) || in.Pressure < 0 || in.Pressure > 1 {
		apiError(w, 400, "invalid_prefetch", "Invalid navigation measurement")
		return
	}
	if in.ByteBudget <= 0 || in.ByteBudget > 16<<20 {
		in.ByteBudget = 16 << 20
	}
	a.costsMu.Lock()
	cost := a.costs[wid]
	a.costsMu.Unlock()
	if cost == 0 {
		cost = .15
	}
	count := int(math.Ceil(in.Velocity*(cost*3+.5))) + in.Visible*2
	count = max(2, min(count, 12))
	if max(in.Pressure, a.cache.Pressure()) > .7 {
		count = max(1, count/2)
	}
	pages := make([]int, 0, count)
	var bytes int64
	for i := range count {
		ordinal := in.Page + in.Direction*(in.Visible+i)
		p, e := a.page(r.Context(), al, ordinal)
		if e != nil {
			break
		}
		estimate := p.Size
		if estimate <= 0 {
			estimate = 4 << 20
		}
		if bytes+estimate > in.ByteBudget {
			break
		}
		bytes += estimate
		pages = append(pages, ordinal)
	}
	ctx, cancel := context.WithTimeout(a.ctx, 20*time.Second)
	generation := id()
	a.prefetchMu.Lock()
	if a.ctx.Err() != nil {
		a.prefetchMu.Unlock()
		cancel()
		apiError(w, 503, "closing", "Server is shutting down")
		return
	}
	if len(a.prefetch) >= 64 {
		a.prefetchMu.Unlock()
		cancel()
		jsonResponse(w, 200, map[string]any{"ok": true, "pages": []int{}, "bytes": 0})
		return
	}
	a.prefetch[key] = cancel
	a.prefetchGeneration[key] = generation
	a.wg.Add(1)
	a.prefetchMu.Unlock()
	go func() {
		defer a.wg.Done()
		defer cancel()
		defer func() {
			a.prefetchMu.Lock()
			if a.prefetchGeneration[key] == generation {
				delete(a.prefetch, key)
				delete(a.prefetchGeneration, key)
			}
			a.prefetchMu.Unlock()
		}()
		select {
		case a.speculative <- struct{}{}:
			defer func() { <-a.speculative }()
		case <-ctx.Done():
			return
		}
		for _, ordinal := range pages {
			if ctx.Err() != nil || !a.authorized(ctx, u, wid) {
				return
			}
			p, e := a.page(ctx, al, ordinal)
			if e != nil {
				return
			}
			_, e = a.loadPage(ctx, u, al, p, Transform{}, true)
			if e != nil {
				return
			}
		}
	}()
	jsonResponse(w, 200, map[string]any{"ok": true, "pages": pages, "bytes": bytes, "policy": fmt.Sprintf("velocity/cost/bytes; %.3fs EWMA", cost)})
}
