package safegroup

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

var ErrPanic = errors.New("panic in a goroutine")

type SafeGroup struct {
	cancel func(error)

	wg sync.WaitGroup

	sem chan token

	errOnce sync.Once
	err     error
}

type token struct{}

func (g *SafeGroup) done() {
	r := recover()
	if r != nil {
		g.errOnce.Do(func() {
			g.err = fmt.Errorf("safegroup: %w: %s", ErrPanic, r)
			if g.cancel != nil {
				g.cancel(g.err)
			}
		})
	}

	if g.sem != nil {
		<-g.sem
	}
	g.wg.Done()
}

func WithContext(ctx context.Context) (*SafeGroup, context.Context) {
	ctx, cancel := context.WithCancelCause(ctx)
	return &SafeGroup{cancel: cancel}, ctx
}

func (g *SafeGroup) Wait() error {
	g.wg.Wait()
	if g.cancel != nil {
		g.cancel(g.err)
	}
	return g.err
}

func (g *SafeGroup) Go(f func() error) {
	if g.sem != nil {
		g.sem <- token{}
	}

	g.wg.Add(1)
	go func() {
		defer g.done()

		if err := f(); err != nil {
			g.errOnce.Do(func() {
				g.err = err
				if g.cancel != nil {
					g.cancel(g.err)
				}
			})
		}
	}()
}

func (g *SafeGroup) TryGo(f func() error) bool {
	if g.sem != nil {
		select {
		case g.sem <- token{}:
		default:
			return false
		}
	}

	g.wg.Add(1)
	go func() {
		defer g.done()

		if err := f(); err != nil {
			g.errOnce.Do(func() {
				g.err = err
				if g.cancel != nil {
					g.cancel(g.err)
				}
			})
		}
	}()
	return true
}

func (g *SafeGroup) SetLimit(n int) {
	if n < 0 {
		g.sem = nil
		return
	}
	if active := len(g.sem); active != 0 {
		panic(fmt.Errorf("safegroup: modify limit while %v goroutines in the group are still active", active))
	}
	g.sem = make(chan token, n)
}
