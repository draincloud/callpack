package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/draincloud/logger"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Config struct {
	Host     string
	Port     string
	Username string
	Password string
	Database string
	AllowSSL bool

	MaxConns        int
	MaxConnIdleTime time.Duration
	MaxConnLifetime time.Duration
}

type DB struct {
	db *pgxpool.Pool
}

type ConnectOpt func(c *Config)

func ConnectDSN(ctx context.Context, dsn string, opts ...ConnectOpt) (*DB, func() error, error) {
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, func() error { return nil }, fmt.Errorf("failed to parse config: %w", err)
	}

	cfg := Config{}
	for _, o := range opts {
		o(&cfg)
	}

	return connect(ctx, config, cfg)
}

func Connect(ctx context.Context, cfg Config, opts ...ConnectOpt) (*DB, func() error, error) {
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, func() error { return nil }, fmt.Errorf("failed to parse config: %w", err)
	}

	config.MaxConnIdleTime = time.Minute * 5
	config.MaxConnLifetime = time.Second * 30
	config.MaxConns = 20

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		logger.FatalKV(ctx, "failed to connect to postgres: %s", err.Error())
	}

	if err := pool.Ping(ctx); err != nil {
		logger.FatalKV(ctx, "failed to ping postgres: %s", err.Error())
	}

	d := &DB{db: pool}
	go d.asyncPing(ctx)

	return d
}

func connect(ctx context.Context, pgconfig *pgxpool.Config, config Config) (*Database, func() error, error) {

}

func (d *Database) Ping(ctx context.Context) error {
	return d.db.Ping(ctx)
}

func (d *Database) asyncPing(ctx context.Context) {
	dur := time.Second
	t := time.NewTicker(dur)
	defer t.Stop()

	for {
		<-t.C
		func() {
			defer t.Reset(dur)
			if err := d.Ping(ctx); err != nil {
				logger.Error(ctx, "Database.asyncPing error", logger.Err(err))
			}
		}()
	}
}

func (d *Database) RunWith(ctx context.Context) ports.DBTX {
	if tx := txFromContext(ctx); tx != nil {
		return tx
	}

	return d.db
}

type txKey struct{}

var ctxKey txKey = txKey{}

var _ ports.DBTX = (*pgx.Conn)(nil)
var _ ports.DBTX = func() pgx.Tx { return nil }()

func (d *Database) WithTransaction(ctx context.Context, fn func(context.Context) error, opts pgx.TxOptions) (err error) {
	tx := txFromContext(ctx)
	if tx == nil {
		tx, err = d.db.BeginTx(ctx, opts)
		if err != nil {
			return fmt.Errorf("failed to begin tx: %w", err)
		}

		defer func() {
			if err == nil {
				err = tx.Commit(ctx)
			}
			if err != nil {
				if rbErr := tx.Rollback(ctx); rbErr != nil {
					err = errors.Join(err, rbErr)
				}
			}
		}()

		ctx = txContext(ctx, tx)
	}

	return fn(ctx)
}

func Conn(ctx context.Context, db ports.DBTX) ports.DBTX {
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
