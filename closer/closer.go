package closer

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/draincloud/logger"
)

type CloseFunc func(ctx context.Context) error

var globalCloser *Closer = &Closer{
	closeFns: make([]CloseFunc, 0),
}

type Closer struct {
	_lock    atomic.Bool
	closeFns []CloseFunc
}

func (c *Closer) Add(fn CloseFunc) {
	if c._lock.Load() {
		return
	}
	c.closeFns = append(c.closeFns, fn)
}

func (c *Closer) Close(ctx context.Context) error {
	if !c._lock.CompareAndSwap(false, true) {
		return errors.New("[closer][Close] already closed")
	}

	var commonErr error
	for _, fn := range c.closeFns {
		if err := fn(ctx); err != nil {
			logger.Error(ctx, "[closer][Close] error at close func call", logger.Err(err))
			commonErr = errors.Join(commonErr, err)
		}
	}

	return commonErr
}

func Add(fn CloseFunc) {
	globalCloser.Add(fn)
}

func Close(ctx context.Context) error {
	return globalCloser.Close(ctx)
}
