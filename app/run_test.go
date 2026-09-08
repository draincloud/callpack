package app_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/draincloud/callpack/app"
)

type runnableFunc func(ctx context.Context) error

func (f runnableFunc) Run(ctx context.Context) error { return f(ctx) }

func TestRunReturnsWhenRunnableExitsCleanly(t *testing.T) {
	t.Parallel()

	blocked := runnableFunc(func(ctx context.Context) error {
		<-ctx.Done()

		return nil
	})
	oneShot := runnableFunc(func(context.Context) error { return nil })

	if err := runWithin(t, time.Second, app.New("test", app.StrategyOneForAll, blocked, oneShot)); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestRunReturnsWhenRunnableOneForOne(t *testing.T) {
	t.Parallel()

	blockedStopped := make(chan struct{})
	blocked := runnableFunc(func(ctx context.Context) error {
		<-ctx.Done()
		close(blockedStopped)

		return nil
	})

	oneCh := make(chan struct{}, 1)
	oneShot := runnableFunc(func(context.Context) error {
		oneCh <- struct{}{}

		return nil
	})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	go func() {
		_ = app.New("test", app.StrategyOneForOne, blocked, oneShot).Run(ctx)
	}()

	select {
	case <-oneCh:
	case <-time.After(time.Second):
		t.Fatal("oneShot did not run")
	}

	// A clean exit under one-for-one must leave the other runnables alone.
	select {
	case <-blockedStopped:
		t.Fatal("blocked app stopped")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestRunWrapsRunnableError(t *testing.T) {
	t.Parallel()

	errBoom := errors.New("boom")
	failing := runnableFunc(func(context.Context) error { return errBoom })

	err := runWithin(t, time.Second, app.New("test", app.StrategyOneForAll, failing))
	if !errors.Is(err, errBoom) {
		t.Fatalf("Run: got %v, want %v", err, errBoom)
	}
}

func TestRunReturnsWhenRootCtxIsCanceledOneForOne(t *testing.T) {
	t.Parallel()

	blocked := runnableFunc(func(ctx context.Context) error {
		return nil
	})

	oneCh := make(chan struct{}, 1)
	defer close(oneCh)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	m := sync.Mutex{}
	stopped := false
	go func() {
		_ = app.New("test", app.StrategyOneForOne, blocked).Run(ctx)
		m.Lock()
		stopped = true
		m.Unlock()
	}()
	cancel()

	<-ticker.C

	m.Lock()
	defer m.Unlock()
	if !stopped {
		t.Fatal("should be stopped but it is not")
	}
}

func runWithin(t *testing.T, d time.Duration, a *app.App) error {
	t.Helper()

	done := make(chan error, 1)
	go func() { done <- a.Run(t.Context()) }()

	select {
	case err := <-done:
		return err
	case <-time.After(d):
		t.Fatal("Run did not return")

		return nil
	}
}
