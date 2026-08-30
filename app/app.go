package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/draincloud/logger"
	"golang.org/x/sync/errgroup"
)

type Runnable interface {
	Run(ctx context.Context) error
}

type App struct {
	name      string
	runnables []Runnable
}

func NewApp(
	name string,
	runnables ...Runnable,
) *App {
	return &App{
		name:      name,
		runnables: runnables,
	}
}

func (a *App) Run(ctx context.Context) error {
	ctx = logger.WithAttrs(ctx, slog.String("app", a.name))
	logger.Warn(ctx, "[App][Run] sstarting app")

	eg, egCtx := errgroup.WithContext(ctx)

	runCtx, cancel := context.WithCancel(egCtx)
	defer cancel()

	for _, r := range a.runnables {
		eg.Go(func() error {
			defer cancel()

			return r.Run(runCtx)
		})
	}

	if err := eg.Wait(); err != nil {
		return fmt.Errorf("[app][Run] %s: %w", a.name, err)
	}

	return nil
}
