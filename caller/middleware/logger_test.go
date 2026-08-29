package middleware_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/draincloud/callpack/caller/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type logRecorder struct {
	mu    sync.Mutex
	lines []logLine
}

type logLine struct {
	ctx  context.Context //nolint:containedctx // the ctx handed to the LogFunc is under test
	msg  string
	args []any
}

func (r *logRecorder) log(ctx context.Context, msg string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, logLine{ctx: ctx, msg: msg, args: args})
}

func (r *logRecorder) all() []logLine {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]logLine{}, r.lines...)
}

func (r *logRecorder) only(t *testing.T) logLine {
	t.Helper()

	lines := r.all()
	require.Lenf(t, lines, 1, "want exactly one log line, got %v", lines)

	return lines[0]
}

func (r *logRecorder) onlyAttrs(t *testing.T) map[string]any {
	t.Helper()
	return r.only(t).attrs(t)
}

func (l logLine) attrs(t *testing.T) map[string]any {
	t.Helper()

	require.Zerof(t, len(l.args)%2, "log args are not key-value pairs: %v", l.args)

	attrs := make(map[string]any, len(l.args)/2)
	for i := 0; i < len(l.args); i += 2 {
		key, ok := l.args[i].(string)
		require.Truef(t, ok, "log arg %d is not a string key, got %T: %v", i, l.args[i], l.args)
		attrs[key] = l.args[i+1]
	}

	return attrs
}

func echoServer(t *testing.T) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if len(body) == 0 {
			body = []byte("pong")
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	return srv
}

func failingTransport(err error) http.RoundTripper {
	return middleware.RoundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, err
	})
}

func request(t *testing.T, ctx context.Context, method, target, body string) *http.Request {
	t.Helper()

	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, target, r)
	require.NoError(t, err)

	return req
}

// roundTrip also returns the body the caller is left with, which the middleware must
// not have consumed.
func roundTrip(t *testing.T, mw middleware.RoundTripperHandler, next http.RoundTripper, req *http.Request) (*http.Response, string, error) {
	t.Helper()

	resp, err := mw(next).RoundTrip(req)
	if resp == nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(resp.Body)
	require.NoError(t, readErr)

	return resp, string(body), err
}

func TestLoggerRequestLine(t *testing.T) {
	rec := &logRecorder{}
	srv := echoServer(t)

	mw := middleware.Logger(middleware.WithLogger(rec.log))
	_, _, err := roundTrip(t, mw, http.DefaultTransport, request(t, t.Context(), http.MethodGet, srv.URL+"/ping", ""))
	require.NoError(t, err)

	line := rec.only(t)
	assert.Equal(t, "client", line.msg)

	attrs := line.attrs(t)
	assert.Equal(t, http.MethodGet, attrs["method"])
	assert.Equal(t, srv.URL+"/ping", attrs["target"])
	assert.NotContains(t, attrs, "headers")
	assert.NotContains(t, attrs, "request body")
}

func TestLoggerDiscardsRequestLineWithoutLogger(t *testing.T) {
	srv := echoServer(t)

	var called bool
	next := middleware.RoundTripperFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return http.DefaultTransport.RoundTrip(r)
	})

	mw := middleware.Logger(middleware.WithRequest(), middleware.WithHeader())
	_, body, err := roundTrip(t, mw, next, request(t, t.Context(), http.MethodPost, srv.URL, "payload"))
	require.NoError(t, err)
	assert.True(t, called, "next transport was not called")
	assert.Equal(t, "payload", body)
}

func TestLoggerWithHeaderRedactsCredentials(t *testing.T) {
	rec := &logRecorder{}
	srv := echoServer(t)

	req := request(t, t.Context(), http.MethodGet, srv.URL, "")
	req.Header.Set("X-Trace-Id", "abc123")
	req.Header.Set("Authorization", "Bearer super-secret")
	req.Header.Set("Cookie", "session=super-secret")

	mw := middleware.Logger(middleware.WithLogger(rec.log), middleware.WithHeader())
	_, _, err := roundTrip(t, mw, http.DefaultTransport, req)
	require.NoError(t, err)

	headers, ok := rec.onlyAttrs(t)["headers"].(string)
	require.True(t, ok, "headers attribute is not a string")
	assert.Contains(t, headers, `"X-Trace-Id":["abc123"]`)
	assert.NotContains(t, headers, "super-secret")
	assert.Contains(t, headers, "Authorization")
	assert.Contains(t, headers, "Cookie")
}

func TestLoggerWithHeaderSkipsEmptyHeaders(t *testing.T) {
	rec := &logRecorder{}

	next := middleware.RoundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	})

	req := request(t, t.Context(), http.MethodGet, "http://example.com", "")
	req.Header = http.Header{}

	mw := middleware.Logger(middleware.WithLogger(rec.log), middleware.WithHeader())
	_, _, err := roundTrip(t, mw, next, req)
	require.NoError(t, err)

	assert.NotContains(t, rec.onlyAttrs(t), "headers")
}

func TestLoggerWithRequestLeavesBodyReadable(t *testing.T) {
	rec := &logRecorder{}
	srv := echoServer(t)

	mw := middleware.Logger(middleware.WithLogger(rec.log), middleware.WithRequest())
	_, echoed, err := roundTrip(t, mw, http.DefaultTransport, request(t, t.Context(), http.MethodPost, srv.URL, `{"id":1}`))
	require.NoError(t, err)

	assert.Equal(t, `{"id":1}`, rec.onlyAttrs(t)["request body"])
	assert.Equal(t, `{"id":1}`, echoed, "server saw a body the logger had consumed")
}

func TestLoggerTruncatesAndFlattensBody(t *testing.T) {
	rec := &logRecorder{}
	srv := echoServer(t)

	body := "head\nline\n" + strings.Repeat("x", 2048)
	mw := middleware.Logger(middleware.WithLogger(rec.log), middleware.WithRequest())
	_, echoed, err := roundTrip(t, mw, http.DefaultTransport, request(t, t.Context(), http.MethodPost, srv.URL, body))
	require.NoError(t, err)

	logged, ok := rec.onlyAttrs(t)["request body"].(string)
	require.True(t, ok, "request body attribute is not a string")
	assert.Len(t, logged, 1024+len("..."))
	assert.True(t, strings.HasSuffix(logged, "..."), "logged body = %q", logged)
	assert.NotContains(t, logged, "\n")
	assert.True(t, strings.HasPrefix(logged, "head line "), "logged body = %q", logged)
	assert.Equal(t, body, echoed, "truncating for the log truncated what was sent")
}

func TestLoggerWithResponse(t *testing.T) {
	rec := &logRecorder{}
	srv := echoServer(t)

	mw := middleware.Logger(middleware.WithResponse(rec.log))
	resp, body, err := roundTrip(t, mw, http.DefaultTransport, request(t, t.Context(), http.MethodGet, srv.URL, ""))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	attrs := rec.onlyAttrs(t)
	assert.Equal(t, http.StatusOK, attrs["code"])
	assert.Equal(t, "pong", attrs["response body"])
	assert.NotEmpty(t, attrs["duration"])
	assert.NotContains(t, attrs, "error")
	assert.Equal(t, "pong", body, "caller was left a body the logger had consumed")
}

func TestLoggerRequestAndResponseAreSeparateLines(t *testing.T) {
	reqRec, respRec := &logRecorder{}, &logRecorder{}
	srv := echoServer(t)

	mw := middleware.Logger(
		middleware.WithLogger(reqRec.log),
		middleware.WithResponse(respRec.log),
	)
	_, _, err := roundTrip(t, mw, http.DefaultTransport, request(t, t.Context(), http.MethodGet, srv.URL, ""))
	require.NoError(t, err)

	assert.NotContains(t, reqRec.onlyAttrs(t), "code")
	assert.Contains(t, respRec.onlyAttrs(t), "code")
}

func TestLoggerTransportErrorGoesToErrorFunc(t *testing.T) {
	reqRec, respRec, errRec := &logRecorder{}, &logRecorder{}, &logRecorder{}
	boom := errors.New("dial tcp: connection refused")

	mw := middleware.Logger(
		middleware.WithLogger(reqRec.log),
		middleware.WithResponse(respRec.log),
		middleware.WithError(errRec.log),
	)
	resp, _, err := roundTrip(t, mw, failingTransport(boom), request(t, t.Context(), http.MethodGet, "http://example.com/x", ""))
	require.ErrorIs(t, err, boom)
	require.Nil(t, resp)

	attrs := errRec.onlyAttrs(t)
	assert.Equal(t, boom.Error(), attrs["error"])
	assert.Equal(t, "http://example.com/x", attrs["target"])
	assert.Contains(t, attrs, "duration")
	assert.NotContains(t, attrs, "code")
	assert.Empty(t, respRec.all(), "response func was called on a failed trip")
}

func TestLoggerTransportErrorWithoutErrorOption(t *testing.T) {
	respRec := &logRecorder{}
	boom := errors.New("boom")

	mw := middleware.Logger(middleware.WithResponse(respRec.log))
	_, _, err := roundTrip(t, mw, failingTransport(boom), request(t, t.Context(), http.MethodGet, "http://example.com", ""))
	require.ErrorIs(t, err, boom)

	assert.Equal(t, boom.Error(), respRec.onlyAttrs(t)["error"])
}

func TestLoggerErrorOptionAloneIsQuietOnSuccess(t *testing.T) {
	errRec := &logRecorder{}
	srv := echoServer(t)

	mw := middleware.Logger(middleware.WithError(errRec.log))
	resp, body, err := roundTrip(t, mw, http.DefaultTransport, request(t, t.Context(), http.MethodGet, srv.URL, ""))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "pong", body)
	assert.Empty(t, errRec.all(), "error func was called on a successful trip")
}

func TestLoggerSkipsRoundTripBookkeepingWhenNothingIsLogged(t *testing.T) {
	rec := &logRecorder{}
	srv := echoServer(t)

	mw := middleware.Logger(middleware.WithLogger(rec.log))
	resp, body, err := roundTrip(t, mw, http.DefaultTransport, request(t, t.Context(), http.MethodGet, srv.URL, ""))
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "pong", body)
	assert.NotContains(t, rec.onlyAttrs(t), "code")
}

func TestLoggerWithPrefix(t *testing.T) {
	rec := &logRecorder{}
	srv := echoServer(t)

	mw := middleware.Logger(middleware.WithLogger(rec.log), middleware.WithPrefix("[consul]"))
	_, _, err := roundTrip(t, mw, http.DefaultTransport, request(t, t.Context(), http.MethodGet, srv.URL, ""))
	require.NoError(t, err)

	assert.Equal(t, "[consul] client", rec.only(t).msg)
}

func TestLoggerWithLogArgsOnEveryLine(t *testing.T) {
	reqRec, respRec := &logRecorder{}, &logRecorder{}
	srv := echoServer(t)

	mw := middleware.Logger(
		middleware.WithLogger(reqRec.log),
		middleware.WithResponse(respRec.log),
		middleware.WithLogArgs("service", "billing"),
	)
	_, _, err := roundTrip(t, mw, http.DefaultTransport, request(t, t.Context(), http.MethodGet, srv.URL, ""))
	require.NoError(t, err)

	for name, rec := range map[string]*logRecorder{"request": reqRec, "response": respRec} {
		assert.Equal(t, "billing", rec.onlyAttrs(t)["service"], "%s line", name)
	}
}

type ctxKey struct{}

func TestLoggerPassesRequestContext(t *testing.T) {
	reqRec, respRec := &logRecorder{}, &logRecorder{}
	srv := echoServer(t)

	ctx := context.WithValue(t.Context(), ctxKey{}, "traced")

	mw := middleware.Logger(
		middleware.WithLogger(reqRec.log),
		middleware.WithResponse(respRec.log),
	)
	_, _, err := roundTrip(t, mw, http.DefaultTransport, request(t, ctx, http.MethodGet, srv.URL, ""))
	require.NoError(t, err)

	for name, rec := range map[string]*logRecorder{"request": reqRec, "response": respRec} {
		assert.Equal(t, "traced", rec.only(t).ctx.Value(ctxKey{}), "%s line", name)
	}
}
