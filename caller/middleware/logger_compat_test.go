package middleware_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/draincloud/callpack/caller"
	"github.com/draincloud/callpack/caller/middleware"
	"github.com/draincloud/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The point of LogFunc is that a context-aware key-value logger can be handed over as
// it is.
var (
	_ middleware.LogFunc = logger.Info
	_ middleware.LogFunc = logger.Debug
	_ middleware.LogFunc = logger.Warn
	_ middleware.LogFunc = logger.Error
)

func logEntries(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()

	var entries []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]any
		require.NoErrorf(t, json.Unmarshal([]byte(line), &entry), "log line is not JSON: %s", line)
		entries = append(entries, entry)
	}

	return entries
}

func TestLoggerWithDrainCloudLogger(t *testing.T) {
	srv := echoServer(t)

	buf := &bytes.Buffer{}
	ctx := logger.NewLoggerContext(t.Context(), logger.WithWriter(buf), logger.WithLevel(logger.LevelInfo))

	c := caller.New(http.Client{}, middleware.Logger(
		middleware.WithLogger(logger.Info),
		middleware.WithHeader(),
		middleware.WithRequest(),
		middleware.WithResponse(logger.Info),
		middleware.WithError(logger.Error),
	))

	req := request(t, ctx, http.MethodPost, srv.URL+"/orders", `{"id":1}`)
	req.Header.Set("X-Trace-Id", "abc123")

	resp, err := c.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	entries := logEntries(t, buf)
	require.Lenf(t, entries, 2, "want a request line and a response line, got %v", entries)

	for _, entry := range entries {
		assert.Equal(t, "client", entry["msg"])
		assert.Equal(t, "INFO", entry["level"])
		assert.Equal(t, http.MethodPost, entry["method"])
		// a *url.URL would land here as a nested object under a JSON handler
		assert.Equal(t, srv.URL+"/orders", entry["target"])
	}

	reqEntry, respEntry := entries[0], entries[1]
	assert.Contains(t, reqEntry["headers"], "X-Trace-Id")
	assert.Equal(t, `{"id":1}`, reqEntry["request body"])
	assert.Equal(t, float64(http.StatusOK), respEntry["code"])
	assert.Equal(t, `{"id":1}`, respEntry["response body"])
	assert.NotEmpty(t, respEntry["duration"])
}

func TestLoggerWithDrainCloudLoggerOnError(t *testing.T) {
	buf := &bytes.Buffer{}
	ctx := logger.NewLoggerContext(t.Context(), logger.WithWriter(buf), logger.WithLevel(logger.LevelInfo))

	boom := errors.New("dial tcp: connection refused")
	mw := middleware.Logger(
		middleware.WithResponse(logger.Info),
		middleware.WithError(logger.Error),
	)
	_, _, err := roundTrip(t, mw, failingTransport(boom), request(t, ctx, http.MethodGet, "http://example.com", ""))
	require.ErrorIs(t, err, boom)

	entries := logEntries(t, buf)
	require.Lenf(t, entries, 1, "want a single outcome line, got %v", entries)
	assert.Equal(t, "ERROR", entries[0]["level"])
	assert.Equal(t, boom.Error(), entries[0]["error"])
}

// The middleware logs through the logger, so its level gate still decides what gets written.
func TestLoggerWithDrainCloudLoggerLevel(t *testing.T) {
	buf := &bytes.Buffer{}
	ctx := logger.NewLoggerContext(t.Context(), logger.WithWriter(buf), logger.WithLevel(logger.LevelError))

	boom := errors.New("boom")
	mw := middleware.Logger(
		middleware.WithLogger(logger.Info),
		middleware.WithResponse(logger.Info),
		middleware.WithError(logger.Error),
	)
	_, _, err := roundTrip(t, mw, failingTransport(boom), request(t, ctx, http.MethodGet, "http://example.com", ""))
	require.ErrorIs(t, err, boom)

	entries := logEntries(t, buf)
	require.Lenf(t, entries, 1, "want only the error line past a LevelError gate, got %v", entries)
	assert.Equal(t, "ERROR", entries[0]["level"])
}
