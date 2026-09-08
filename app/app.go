package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/draincloud/callpack/safegroup"
	"github.com/draincloud/logger"
)

type Runnable interface {
	Run(ctx context.Context) error
}

type App struct {
	name      string
	strategy  Strategy
	runnables []Runnable
}

type Strategy string

const (
	// If one exits - all runnables will be killed with cancel
	StrategyOneForAll Strategy = "one_for_all"
	// If one exits WITHOUT error - there will be no context cancel. Error will still cause context cancellation.
	StrategyOneForOne Strategy = "one_for_one"
)

func New(
	name string,
	strategy Strategy,
	runnables ...Runnable,
) *App {
	return &App{
		name:      name,
		strategy:  strategy,
		runnables: runnables,
	}
}

func (a *App) Run(ctx context.Context) error {
	ctx = logger.WithAttrs(ctx, slog.String("app", a.name))
	logger.Warn(ctx, "[App][Run] starting app")

	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	errChan := make(chan error, 1)
	defer close(errChan)

	stopChan := make(chan struct{}, 1)
	defer close(stopChan)

	eg, egCtx := safegroup.WithContext(ctx)

	runCtx, runCancel := context.WithCancel(egCtx)
	defer runCancel()

	for _, r := range a.runnables {
		eg.Go(func() error {
			if a.strategy == StrategyOneForAll {
				defer runCancel()
			}
			return r.Run(runCtx)
		})
	}

	go func() {
		defer cancel()
		if err := eg.Wait(); err != nil {
			errChan <- fmt.Errorf("[app][Run] %s: %w", a.name, err)
			return
		}
		stopChan <- struct{}{}
	}()

	select {
	case err := <-errChan:
		return err
	case <-stopChan:
		return nil
	}
}
