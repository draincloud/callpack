package postgres

import (
	"context"
	"errors"
	"testing"

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
