package app_test

import (
	"context"
	"errors"
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

	if err := runWithin(t, time.Second, app.NewApp("test", blocked, oneShot)); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestRunWrapsRunnableError(t *testing.T) {
	t.Parallel()

	errBoom := errors.New("boom")
	failing := runnableFunc(func(context.Context) error { return errBoom })

	err := runWithin(t, time.Second, app.NewApp("test", failing))
	if !errors.Is(err, errBoom) {
		t.Fatalf("Run: got %v, want %v", err, errBoom)
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
