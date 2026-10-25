package app

import (
	"context"
	"sync/atomic"
)

type backgroundKey struct{}
type priorityKey struct{}
type prioritySignal struct {
	foreground atomic.Bool
	parent     *prioritySignal
}

func background(ctx context.Context) bool {
	value, _ := ctx.Value(backgroundKey{}).(bool)
	return value
}
func signalFor(ctx context.Context) *prioritySignal {
	value, _ := ctx.Value(priorityKey{}).(*prioritySignal)
	return value
}
func promoted(ctx context.Context) bool {
	for signal := signalFor(ctx); signal != nil; signal = signal.parent {
		if signal.foreground.Load() {
			return true
		}
	}
	return false
}
