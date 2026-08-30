package closer

import (
	"context"
	"errors"
	"sync"

	"github.com/draincloud/logger"
)

type CloseFunc func(ctx context.Context) error

// ErrClosed is returned by Add once Close has started; the cleanup is not registered.
var ErrClosed = errors.New("[closer] already closed")

var globalCloser = &Closer{}

type Closer struct {
	mu       sync.Mutex
	closed   bool
	done     chan struct{}
	err      error
	closeFns []CloseFunc
}

func (c *Closer) Add(fn CloseFunc) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return ErrClosed
	}
	c.closeFns = append(c.closeFns, fn)

	return nil
}

// Close runs the registered cleanups in reverse registration order, so dependents
// are torn down before the resources they use. Concurrent callers block until the
// first Close finishes and receive its result.
func (c *Closer) Close(ctx context.Context) error {
	c.mu.Lock()
	if c.closed {
		done := c.done
		c.mu.Unlock()
		<-done

		return c.err
	}
	c.closed = true
	c.done = make(chan struct{})
	done, fns := c.done, c.closeFns
	c.mu.Unlock()

	var commonErr error
	for i := len(fns) - 1; i >= 0; i-- {
		if err := fns[i](ctx); err != nil {
			logger.Error(ctx, "[closer][Close] error at close func call", logger.Err(err))
			commonErr = errors.Join(commonErr, err)
		}
	}

	c.err = commonErr
	close(done)

	return commonErr
}

func Add(fn CloseFunc) error {
	return globalCloser.Add(fn)
}

func Close(ctx context.Context) error {
	return globalCloser.Close(ctx)
}
