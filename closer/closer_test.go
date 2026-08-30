package closer_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/draincloud/callpack/closer"
)

func TestCloserAddConcurrent(t *testing.T) {
	t.Parallel()

	const n = 50
	c := &closer.Closer{}
	var calls atomic.Int64
	var wg sync.WaitGroup
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			if err := c.Add(func(context.Context) error {
				calls.Add(1)

				return nil
			}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()

	if err := c.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := calls.Load(); got != n {
		t.Fatalf("ran %d cleanups, want %d", got, n)
	}
}

func TestCloserAddAfterClose(t *testing.T) {
	t.Parallel()

	c := &closer.Closer{}
	if err := c.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if err := c.Add(func(context.Context) error { return nil }); !errors.Is(err, closer.ErrClosed) {
		t.Fatalf("Add after Close: got %v, want ErrClosed", err)
	}
}

func TestCloserClosesInReverseOrder(t *testing.T) {
	t.Parallel()

	c := &closer.Closer{}
	var order []int
	for i := range 3 {
		if err := c.Add(func(context.Context) error {
			order = append(order, i)

			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := c.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if want := []int{2, 1, 0}; !slices.Equal(order, want) {
		t.Fatalf("close order %v, want %v", order, want)
	}
}

func TestCloserConcurrentCloseWaits(t *testing.T) {
	t.Parallel()

	errCleanup := errors.New("cleanup failed")
	released := make(chan struct{})
	var done atomic.Bool

	c := &closer.Closer{}
	if err := c.Add(func(context.Context) error {
		<-released
		done.Store(true)

		return errCleanup
	}); err != nil {
		t.Fatal(err)
	}

	first := make(chan error, 1)
	go func() { first <- c.Close(t.Context()) }()

	// Let the first Close reach the blocking cleanup before the second starts.
	time.Sleep(10 * time.Millisecond)
	second := make(chan error, 1)
	go func() { second <- c.Close(t.Context()) }()

	select {
	case err := <-second:
		t.Fatalf("second Close returned %v while cleanups were still running", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(released)
	for _, ch := range []chan error{first, second} {
		select {
		case err := <-ch:
			if !errors.Is(err, errCleanup) {
				t.Fatalf("Close: got %v, want %v", err, errCleanup)
			}
		case <-time.After(time.Second):
			t.Fatal("Close did not return")
		}
	}
	if !done.Load() {
		t.Fatal("cleanup did not finish")
	}
}
