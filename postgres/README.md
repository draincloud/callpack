# Postgres wrapper
Usage example:
main.go:
```go
func main() {
    config := readConfig(ctx) // read from app config.

    db, closeDB, err := postgres.Connect(ctx, config)
    if err != nil {
        panic(err)
    }
    defer closeDB(ctx)

    data := getDataToFill(ctx) // get some data

    storage := storage.New(db)

    if err = storage.Backfill(ctx, data); err != nil {
        panic(err)
    }
}
```
storage.go:
```go
type Storage struct {
    db *postgres.DB
}

func New(db *postgres.DB) *Storage {
    return &Storage{db: db}
}

func (s *Storage) Backfill(ctx context.Context, data map[int64]string) error {
    return s.db.WithTransaction(ctx, func(ctx context.Context) error {
        query := `update table set key = $1 where id = $2;`
        for id, key := range data {
            if _, err := s.db.RunWith(ctx).Exec(ctx, query, key, id); err != nil {
                return err
            }
        }
        return nil
    }, pgx.TxOptions{})
}
```

`Connect` and `ConnectDSN` take options:
```go
db, closeDB, err := postgres.Connect(ctx, config,
    postgres.WithLogger(slog.Default().ErrorContext),
    postgres.WithTracer(&tracelog.TraceLog{Logger: tracelog.LoggerFunc(logQuery)}),
)
```

`WithLogger` redirects the background health check, which otherwise reports failures to
the global logger. `WithTracer` takes a `pgx.QueryTracer`; pgx also uses it for the batch,
copy, prepare, connect, acquire and release traces it implements, so `otelpgx` and
`pgx/v5/tracelog` both go through this one option.

`closeDB` shuts the pool down and stops the background health check; it matches
`closer.CloseFunc`, so it can be handed to `closer.Add` directly.

If WithTransaction called inside WithTransaction callback, it will reuse top-level transaction.
Passing non-zero `pgx.TxOptions` to a nested call returns `ErrNestedTxOptions`, since the
options cannot be applied to the transaction that is already running.
