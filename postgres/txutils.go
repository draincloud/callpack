package postgres

import (
	"github.com/jackc/pgx/v5"
)

type txKey struct{}

var ctxKey txKey = txKey{}

var _ DBTX = (*pgx.Conn)(nil)
var _ DBTX = func() pgx.Tx { return nil }()
