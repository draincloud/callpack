package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"time"

	"github.com/draincloud/logger"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNestedTxOptions = errors.New("tx options cannot be applied to an already running transaction")

type Config struct {
	Host     string
	Port     string
	Username string
	Password string
	Database string
	SSLMode  string

	MaxConns        int
	MaxConnIdleTime time.Duration
	MaxConnLifetime time.Duration

	logger       *slog.Logger
	tracer       pgx.QueryTracer
	afterConnect func(context.Context, *pgx.Conn) error
}

func (c Config) dsn() string {
	host := c.Host
	if c.Port != "" {
		host = net.JoinHostPort(c.Host, c.Port)
	}

	u := url.URL{
		Scheme: "postgres",
		Host:   host,
		Path:   "/" + c.Database,
	}

	if c.Username != "" {
		u.User = url.UserPassword(c.Username, c.Password)
	}

	if c.SSLMode != "" {
		u.RawQuery = url.Values{"sslmode": {c.SSLMode}}.Encode()
	}

	return u.String()
}

type DB struct {
	db  *pgxpool.Pool
	log *slog.Logger
}

type ConnectOpt func(c *Config)

func WithLogger(logger *slog.Logger) ConnectOpt {
	return func(c *Config) {
		c.logger = logger
	}
}

func WithTracer(t pgx.QueryTracer) ConnectOpt {
	return func(c *Config) {
		c.tracer = t
	}
}

func WithAfterConnect(fn func(ctx context.Context, conn *pgx.Conn) error) ConnectOpt {
	return func(c *Config) {
		c.afterConnect = fn
	}
}

func WithMaxConns(n int) ConnectOpt {
	return func(c *Config) {
		c.MaxConns = n
	}
}

func WithMaxConnIdleTime(d time.Duration) ConnectOpt {
	return func(c *Config) {
		c.MaxConnIdleTime = d
	}
}

func WithMaxConnLifetime(d time.Duration) ConnectOpt {
	return func(c *Config) {
		c.MaxConnLifetime = d
	}
}

func ConnectDSN(ctx context.Context, dsn string, opts ...ConnectOpt) (*DB, func(context.Context) error, error) {
	pgconfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, noopCloser, fmt.Errorf("failed to parse config: %w", err)
	}

	cfg := Config{}
	for _, o := range opts {
		o(&cfg)
	}

	return connect(ctx, pgconfig, cfg)
}

func Connect(ctx context.Context, cfg Config, opts ...ConnectOpt) (*DB, func(context.Context) error, error) {
	for _, o := range opts {
		o(&cfg)
	}

	pgconfig, err := pgxpool.ParseConfig(cfg.dsn())
	if err != nil {
		return nil, noopCloser, fmt.Errorf("failed to parse config: %w", err)
	}

	return connect(ctx, pgconfig, cfg)
}

func noopCloser(context.Context) error { return nil }

func connect(ctx context.Context, pgconfig *pgxpool.Config, cfg Config) (*DB, func(context.Context) error, error) {
	if cfg.MaxConns > 0 {
		pgconfig.MaxConns = int32(cfg.MaxConns)
	}

	if cfg.MaxConnIdleTime > 0 {
		pgconfig.MaxConnIdleTime = cfg.MaxConnIdleTime
	}

	if cfg.MaxConnLifetime > 0 {
		pgconfig.MaxConnLifetime = cfg.MaxConnLifetime
	}

	if cfg.tracer != nil {
		pgconfig.ConnConfig.Tracer = cfg.tracer
	}

	if cfg.afterConnect != nil {
		pgconfig.AfterConnect = cfg.afterConnect
	}

	pool, err := pgxpool.NewWithConfig(ctx, pgconfig)
	if err != nil {
		return nil, noopCloser, fmt.Errorf("failed to connect to postgres: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()

		return nil, noopCloser, fmt.Errorf("failed to ping postgres: %w", err)
	}

	pingCtx, cancelPing := context.WithCancel(context.WithoutCancel(ctx))

	log := cfg.logger
	if log == nil {
		log = logger.FromContext(ctx)
	}
	setupLogger(log)

	d := &DB{db: pool, log: log}
	go d.asyncPing(pingCtx)

	return d, func(context.Context) error {
		cancelPing()
		pool.Close()

		return nil
	}, nil
}

func (d *DB) Ping(ctx context.Context) error {
	if err := d.db.Ping(ctx); err != nil {
		return fmt.Errorf("failed to ping postgres: %w", err)
	}

	return nil
}

func (d *DB) asyncPing(ctx context.Context) {
	dur := time.Second
	t := time.NewTicker(dur)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}

		func() {
			defer t.Reset(dur)
			if err := d.Ping(ctx); err != nil {
				d.log.Error("DB.asyncPing error", logger.Err(err))
			}
		}()
	}
}

func (d *DB) RunWith(ctx context.Context) DBTX {
	if tx := txFromContext(ctx); tx != nil {
		return tx
	}

	return d.db
}

func (d *DB) WithTransaction(ctx context.Context, fn func(context.Context) error, opts pgx.TxOptions) (err error) {
	if tx := txFromContext(ctx); tx != nil {
		if opts != (pgx.TxOptions{}) {
			return ErrNestedTxOptions
		}

		return fn(ctx)
	}

	tx, err := d.db.BeginTx(ctx, opts)
	if err != nil {
		return fmt.Errorf("failed to begin tx: %w", err)
	}

	defer func() {
		closeCtx := context.WithoutCancel(ctx)

		if p := recover(); p != nil {
			_ = tx.Rollback(closeCtx)

			panic(p)
		}

		if err != nil {
			if rbErr := tx.Rollback(closeCtx); rbErr != nil {
				err = errors.Join(err, rbErr)
			}

			return
		}

		if cErr := tx.Commit(closeCtx); cErr != nil {
			err = fmt.Errorf("failed to commit tx: %w", cErr)
		}
	}()

	return fn(txContext(ctx, tx))
}

func InTransaction(ctx context.Context) bool {
	return txFromContext(ctx) != nil
}

func Conn(ctx context.Context, db DBTX) DBTX {
	if tx := txFromContext(ctx); tx != nil {
		return tx
	}

	return db
}

func txFromContext(ctx context.Context) pgx.Tx {
	if tx, ok := ctx.Value(ctxKey).(pgx.Tx); ok {
		return tx
	}

	return nil
}

func txContext(parent context.Context, tx pgx.Tx) context.Context {
	return context.WithValue(parent, ctxKey, tx)
}

func setupLogger(log *slog.Logger) {
	log.With(slog.String("system", "postgres.database"))
}
