package postgres

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestConfigDSN(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{
			name: "full",
			cfg:  Config{Host: "db.local", Port: "5432", Username: "us er", Password: "p@ss:/word", Database: "app", SSLMode: "verify-full"},
			want: "postgres://us%20er:p%40ss%3A%2Fword@db.local:5432/app?sslmode=verify-full",
		},
		{
			name: "no ssl mode leaves the pgx default",
			cfg:  Config{Host: "localhost", Database: "app"},
			want: "postgres://localhost/app",
		},
		{
			name: "no port",
			cfg:  Config{Host: "localhost", Username: "u", Password: "p", Database: "app"},
			want: "postgres://u:p@localhost/app",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.dsn(); got != tt.want {
				t.Fatalf("dsn() = %q, want %q", got, tt.want)
			}

			if _, err := pgxpool.ParseConfig(tt.cfg.dsn()); err != nil {
				t.Fatalf("ParseConfig(%q) = %v", tt.cfg.dsn(), err)
			}
		})
	}
}

func TestConfigDSNDefaultsToPrefer(t *testing.T) {
	pgconfig, err := pgxpool.ParseConfig(Config{Host: "localhost", Database: "app"}.dsn())
	if err != nil {
		t.Fatal(err)
	}

	if pgconfig.ConnConfig.TLSConfig == nil {
		t.Fatal("expected TLS to remain available when SSLMode is unset")
	}
}

type stubTx struct{ pgx.Tx }

type stubDBTX struct{ DBTX }

func TestWithTransactionNestedRejectsOptions(t *testing.T) {
	ctx := txContext(context.Background(), stubTx{})

	err := (&DB{}).WithTransaction(ctx, func(context.Context) error {
		t.Fatal("callback must not run when options conflict")

		return nil
	}, pgx.TxOptions{IsoLevel: pgx.Serializable})

	if !errors.Is(err, ErrNestedTxOptions) {
		t.Fatalf("err = %v, want ErrNestedTxOptions", err)
	}
}

func TestWithTransactionNestedReusesOuterTx(t *testing.T) {
	tx := stubTx{}
	ctx := txContext(context.Background(), tx)

	var got pgx.Tx

	err := (&DB{}).WithTransaction(ctx, func(ctx context.Context) error {
		got = txFromContext(ctx)

		return nil
	}, pgx.TxOptions{})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}

	if got != pgx.Tx(tx) {
		t.Fatal("nested call did not reuse the outer transaction")
	}
}

func TestConnectAppliesOptsBeforeBuildingDSN(t *testing.T) {
	cfg := Config{Host: "127.0.0.1", Port: "1", Database: "app"}

	db, closeDB, err := Connect(t.Context(), cfg, func(c *Config) { c.SSLMode = "bogus" })
	if err == nil {
		closeDB(t.Context())
		t.Fatal("expected the option to reach the DSN and fail to parse")
	}

	if db != nil {
		t.Fatalf("db = %v, want nil", db)
	}

	if err := closeDB(t.Context()); err != nil {
		t.Fatalf("closer returned on failure: %v", err)
	}
}

func TestConnectUnreachableHost(t *testing.T) {
	db, closeDB, err := Connect(t.Context(), Config{Host: "127.0.0.1", Port: "1", Database: "app", SSLMode: "disable"})
	if err == nil {
		closeDB(t.Context())
		t.Fatal("expected the ping to fail")
	}

	if db != nil {
		t.Fatalf("db = %v, want nil", db)
	}

	if err := closeDB(t.Context()); err != nil {
		t.Fatalf("closer returned on failure: %v", err)
	}
}

func TestConnectDSNRejectsUnparseableDSN(t *testing.T) {
	db, closeDB, err := ConnectDSN(t.Context(), "postgres://host:port/app")
	if err == nil {
		closeDB(t.Context())
		t.Fatal("expected an unparseable DSN to fail")
	}

	if db != nil {
		t.Fatalf("db = %v, want nil", db)
	}

	if err := closeDB(t.Context()); err != nil {
		t.Fatalf("closer returned on failure: %v", err)
	}
}

func TestConnectAppliesPoolTuning(t *testing.T) {
	pgconfig, err := pgxpool.ParseConfig(Config{Host: "127.0.0.1", Port: "1", Database: "app"}.dsn())
	if err != nil {
		t.Fatal(err)
	}

	cfg := Config{MaxConns: 7, MaxConnIdleTime: time.Minute, MaxConnLifetime: time.Hour}
	if _, closeDB, err := connect(t.Context(), pgconfig, cfg); err == nil {
		closeDB(t.Context())
		t.Fatal("expected the ping to fail")
	}

	if pgconfig.MaxConns != int32(cfg.MaxConns) {
		t.Fatalf("MaxConns = %d, want %d", pgconfig.MaxConns, cfg.MaxConns)
	}

	if pgconfig.MaxConnIdleTime != cfg.MaxConnIdleTime {
		t.Fatalf("MaxConnIdleTime = %s, want %s", pgconfig.MaxConnIdleTime, cfg.MaxConnIdleTime)
	}

	if pgconfig.MaxConnLifetime != cfg.MaxConnLifetime {
		t.Fatalf("MaxConnLifetime = %s, want %s", pgconfig.MaxConnLifetime, cfg.MaxConnLifetime)
	}
}

func TestConnectLeavesPgxDefaultsWhenUntuned(t *testing.T) {
	pgconfig, err := pgxpool.ParseConfig(Config{Host: "127.0.0.1", Port: "1", Database: "app"}.dsn())
	if err != nil {
		t.Fatal(err)
	}

	want := *pgconfig

	if _, closeDB, err := connect(t.Context(), pgconfig, Config{}); err == nil {
		closeDB(t.Context())
		t.Fatal("expected the ping to fail")
	}

	if pgconfig.MaxConns != want.MaxConns ||
		pgconfig.MaxConnIdleTime != want.MaxConnIdleTime ||
		pgconfig.MaxConnLifetime != want.MaxConnLifetime {
		t.Fatalf("zero-value Config overwrote the pgx defaults: %+v", pgconfig)
	}
}

func TestRunWith(t *testing.T) {
	db := &DB{}

	got := db.RunWith(t.Context())
	if pool, ok := got.(*pgxpool.Pool); !ok || pool != db.db {
		t.Fatalf("RunWith without a transaction = %#v, want the pool", got)
	}

	tx := stubTx{}
	if got := db.RunWith(txContext(t.Context(), tx)); got != DBTX(tx) {
		t.Fatalf("RunWith inside a transaction = %#v, want the transaction", got)
	}
}

func TestConn(t *testing.T) {
	db := stubDBTX{}

	if got := Conn(t.Context(), db); got != DBTX(db) {
		t.Fatalf("Conn without a transaction = %#v, want the given DBTX", got)
	}

	tx := stubTx{}
	if got := Conn(txContext(t.Context(), tx), db); got != DBTX(tx) {
		t.Fatalf("Conn inside a transaction = %#v, want the transaction", got)
	}
}

func TestWithTransactionNestedPropagatesError(t *testing.T) {
	errCallback := errors.New("callback failed")
	ctx := txContext(t.Context(), stubTx{})

	err := (&DB{}).WithTransaction(ctx, func(context.Context) error {
		return errCallback
	}, pgx.TxOptions{})

	if !errors.Is(err, errCallback) {
		t.Fatalf("err = %v, want %v", err, errCallback)
	}
}

func TestConnectDSNAppliesOpts(t *testing.T) {
	applied := false

	db, closeDB, err := ConnectDSN(t.Context(), "postgres://127.0.0.1:1/app?sslmode=disable", func(c *Config) {
		applied = true
		c.MaxConns = 3
	})
	if err == nil {
		closeDB(t.Context())
		t.Fatal("expected the ping to fail")
	}

	if db != nil {
		t.Fatalf("db = %v, want nil", db)
	}

	if !applied {
		t.Fatal("options are not applied on the DSN path")
	}
}

func newLazyPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	pool, err := pgxpool.New(t.Context(), "postgres://127.0.0.1:1/app?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(pool.Close)

	return pool
}

func TestPingWrapsError(t *testing.T) {
	err := (&DB{db: newLazyPool(t)}).Ping(t.Context())
	if err == nil {
		t.Fatal("expected the ping to fail")
	}

	if !strings.Contains(err.Error(), "failed to ping postgres") {
		t.Fatalf("err = %v, want it named by this package", err)
	}
}

func TestAsyncPingStopsWhenContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		(&DB{db: newLazyPool(t), log: slog.New(slog.DiscardHandler)}).asyncPing(ctx)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("asyncPing outlived its context")
	}
}

type stubTracer struct{}

func (stubTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	return ctx
}

func (stubTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestWithTracerReachesTheConnConfig(t *testing.T) {
	pgconfig, err := pgxpool.ParseConfig(Config{Host: "127.0.0.1", Port: "1", Database: "app"}.dsn())
	if err != nil {
		t.Fatal(err)
	}

	tracer := stubTracer{}

	cfg := Config{}
	WithTracer(tracer)(&cfg)

	if _, closeDB, err := connect(t.Context(), pgconfig, cfg); err == nil {
		closeDB(t.Context())
		t.Fatal("expected the ping to fail")
	}

	if pgconfig.ConnConfig.Tracer != pgx.QueryTracer(tracer) {
		t.Fatalf("Tracer = %#v, want the tracer the option was given", pgconfig.ConnConfig.Tracer)
	}
}

func TestWithTracerLeavesTheConnConfigAloneWhenUnset(t *testing.T) {
	pgconfig, err := pgxpool.ParseConfig(Config{Host: "127.0.0.1", Port: "1", Database: "app"}.dsn())
	if err != nil {
		t.Fatal(err)
	}

	if _, closeDB, err := connect(t.Context(), pgconfig, Config{}); err == nil {
		closeDB(t.Context())
		t.Fatal("expected the ping to fail")
	}

	if pgconfig.ConnConfig.Tracer != nil {
		t.Fatalf("Tracer = %#v, want nil", pgconfig.ConnConfig.Tracer)
	}
}

// capturingHandler forwards each record's message to a channel, so a test can
// assert on what the configured logger was asked to write.
type capturingHandler struct {
	msgs chan<- string
}

func (capturingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h capturingHandler) Handle(_ context.Context, r slog.Record) error {
	select {
	case h.msgs <- r.Message:
	default:
	}

	return nil
}

func (h capturingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h capturingHandler) WithGroup(string) slog.Handler { return h }

func TestWithLoggerReceivesHealthCheckFailures(t *testing.T) {
	logged := make(chan string, 1)

	cfg := Config{}
	WithLogger(slog.New(capturingHandler{msgs: logged}))(&cfg)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	db := &DB{db: newLazyPool(t), log: cfg.logger}

	done := make(chan struct{})
	go func() {
		defer close(done)
		db.asyncPing(ctx)
	}()

	select {
	case msg := <-logged:
		if !strings.Contains(msg, "asyncPing") {
			t.Fatalf("logged %q, want the health check failure", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the configured logger never saw the failed health check")
	}

	cancel()
	<-done
}

func TestWithAfterConnectReachesThePoolConfig(t *testing.T) {
	pgconfig, err := pgxpool.ParseConfig(Config{Host: "127.0.0.1", Port: "1", Database: "app"}.dsn())
	if err != nil {
		t.Fatal(err)
	}

	sentinel := errors.New("sentinel")

	cfg := Config{}
	WithAfterConnect(func(context.Context, *pgx.Conn) error { return sentinel })(&cfg)

	if _, closeDB, err := connect(t.Context(), pgconfig, cfg); err == nil {
		closeDB(t.Context())
		t.Fatal("expected the ping to fail")
	}

	if pgconfig.AfterConnect == nil {
		t.Fatal("AfterConnect = nil, want the hook the option was given")
	}

	// Compared by what it does: func values are not comparable.
	if err := pgconfig.AfterConnect(t.Context(), nil); !errors.Is(err, sentinel) {
		t.Fatalf("AfterConnect returned %v, want the hook the option was given", err)
	}
}

func TestWithAfterConnectLeavesThePoolConfigAloneWhenUnset(t *testing.T) {
	pgconfig, err := pgxpool.ParseConfig(Config{Host: "127.0.0.1", Port: "1", Database: "app"}.dsn())
	if err != nil {
		t.Fatal(err)
	}

	if _, closeDB, err := connect(t.Context(), pgconfig, Config{}); err == nil {
		closeDB(t.Context())
		t.Fatal("expected the ping to fail")
	}

	if pgconfig.AfterConnect != nil {
		t.Fatal("AfterConnect is set, want nil")
	}
}

func TestPoolLimitOptionsReachThePoolConfig(t *testing.T) {
	pgconfig, err := pgxpool.ParseConfig(Config{Host: "127.0.0.1", Port: "1", Database: "app"}.dsn())
	if err != nil {
		t.Fatal(err)
	}

	cfg := Config{}
	for _, o := range []ConnectOpt{
		WithMaxConns(20),
		WithMaxConnIdleTime(5 * time.Minute),
		WithMaxConnLifetime(30 * time.Second),
	} {
		o(&cfg)
	}

	if _, closeDB, err := connect(t.Context(), pgconfig, cfg); err == nil {
		closeDB(t.Context())
		t.Fatal("expected the ping to fail")
	}

	if pgconfig.MaxConns != 20 {
		t.Fatalf("MaxConns = %d, want 20", pgconfig.MaxConns)
	}

	if pgconfig.MaxConnIdleTime != 5*time.Minute {
		t.Fatalf("MaxConnIdleTime = %v, want 5m", pgconfig.MaxConnIdleTime)
	}

	if pgconfig.MaxConnLifetime != 30*time.Second {
		t.Fatalf("MaxConnLifetime = %v, want 30s", pgconfig.MaxConnLifetime)
	}
}

func TestInTransactionTracksTheContext(t *testing.T) {
	if InTransaction(t.Context()) {
		t.Fatal("InTransaction = true on a bare context")
	}

	ctx := txContext(t.Context(), nil)
	if InTransaction(ctx) {
		t.Fatal("InTransaction = true for a nil transaction")
	}
}
