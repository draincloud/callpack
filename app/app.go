package app

import (
	"context"

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
	eg, egCtx := errgroup.WithContext(ctx)

	for _, r := range a.runnables {
		eg.Go(func() error {
			return r.Run(egCtx)
		})
	}

	return eg.Wait()
}
