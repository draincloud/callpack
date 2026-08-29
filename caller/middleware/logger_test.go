package middleware

import (
	"log/slog"
	"net/http"
	"testing"
)

func TestLogger(t *testing.T) {
	r := &http.Request{
		Header: http.Header{
			"h1": []string{"1", "2", "3"},
			"h2": []string{"qwe"},
		},
	}

	slog.Info("123", "headers", r.Header)
}
