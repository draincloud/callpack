package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
)

// maxBodyLog is how much of a body a log line keeps; anything longer is truncated.
const maxBodyLog = 1024

// LogFunc is the shape of a context-aware key-value logging call, matching
// slog.Logger.InfoContext and friends so a logger can be handed over directly.
type LogFunc func(context.Context, string, ...any)

type loggerConfig struct {
	logFunc     LogFunc
	errLogFunc  LogFunc
	respLogFunc LogFunc

	logHeaders, logReq, logResp, logErr bool

	prefix string
	args   []any
}

func newDefaulLoggerConfig() *loggerConfig {
	return &loggerConfig{
		logFunc: DiscardLoggerFunc,
	}
}

type LoggerOption func(d *loggerConfig)

// WithLogger sets the function the request line is logged with. Without it requests are
// discarded, leaving WithRequest and WithHeader nothing to log to.
func WithLogger(f LogFunc) LoggerOption {
	return func(d *loggerConfig) {
		d.logFunc = f
	}
}

// WithRequest adds the request body to the request line.
func WithRequest() LoggerOption {
	return func(d *loggerConfig) {
		d.logReq = true
	}
}

// WithHeader adds the request headers to the request line, with the values of headers
// that carry credentials redacted.
func WithHeader() LoggerOption {
	return func(d *loggerConfig) {
		d.logHeaders = true
	}
}

// WithResponse logs the outcome of the round trip - status code, duration and response
// body - with f.
func WithResponse(f LogFunc) LoggerOption {
	return func(d *loggerConfig) {
		d.logResp = true
		d.respLogFunc = f
	}
}

// WithError logs a failed round trip with f instead of the response function, so
// failures can go to a louder level than successes.
func WithError(f LogFunc) LoggerOption {
	return func(d *loggerConfig) {
		d.logErr = true
		d.errLogFunc = f
	}
}

// WithPrefix puts prefix in front of every message this middleware logs.
func WithPrefix(prefix string) LoggerOption {
	return func(d *loggerConfig) {
		d.prefix = prefix
	}
}

// WithLogArgs adds key-value pairs to every line this middleware logs.
func WithLogArgs(args ...any) LoggerOption {
	return func(d *loggerConfig) {
		d.args = args
	}
}

func DiscardLoggerFunc(_ context.Context, _ string, _ ...any) {}

// Logger logs outgoing requests and their outcome. A round trip logs one request line
// and, once WithResponse or WithError is set, exactly one outcome line: the error
// function when the trip failed and WithError is set, the response function otherwise.
func Logger(opts ...LoggerOption) func(http.RoundTripper) http.RoundTripper {
	cfg := newDefaulLoggerConfig()
	for _, o := range opts {
		o(cfg)
	}

	return func(next http.RoundTripper) http.RoundTripper {
		return RoundTripperFunc(func(r *http.Request) (*http.Response, error) {
			st := time.Now()

			logMessage := fmt.Sprintf("%sclient", wrapPrefix(cfg.prefix))
			// Redacted keeps a password embedded in the URL out of the log, and renders as a
			// string under a JSON handler, where a *url.URL would land as a struct
			logParts := append([]any{"method", r.Method, "target", r.URL.Redacted()}, cfg.args...)

			reqLogParts := slices.Clone(logParts)
			if cfg.logHeaders && len(r.Header) > 0 {
				reqLogParts = append(reqLogParts, "headers", headerLog(r.Header))
			}
			if cfg.logReq && r.Body != nil {
				var bodyLog string
				bodyLog, r.Body = drainForLog(r.Body)
				if bodyLog != "" {
					reqLogParts = append(reqLogParts, "request body", bodyLog)
				}
			}
			cfg.logFunc(r.Context(), logMessage, reqLogParts...)

			if !cfg.logResp && !cfg.logErr {
				return next.RoundTrip(r)
			}

			resp, err := next.RoundTrip(r)
			outLogParts := append(slices.Clone(logParts), "duration", time.Since(st).String())

			// a failed round trip has no response to report on, so the outcome line is
			// the error - logged by the error function when there is one
			if err != nil {
				outLogParts = append(outLogParts, "error", err.Error())
				outLogFunc := cfg.respLogFunc
				if cfg.logErr {
					outLogFunc = cfg.errLogFunc
				}
				outLogFunc(r.Context(), logMessage, outLogParts...)
				return resp, err
			}

			if !cfg.logResp {
				return resp, nil
			}

			outLogParts = append(outLogParts, "code", resp.StatusCode)
			if resp.Body != nil {
				var bodyLog string
				bodyLog, resp.Body = drainForLog(resp.Body)
				if bodyLog != "" {
					outLogParts = append(outLogParts, "response body", bodyLog)
				}
			}
			cfg.respLogFunc(r.Context(), logMessage, outLogParts...)

			return resp, nil
		})
	}
}

// drainForLog reads body and returns a single-line, truncated copy of it for logging
// along with a replacement left readable by the next hop. A body that cannot be read is
// handed back untouched and logged as empty.
func drainForLog(body io.ReadCloser) (string, io.ReadCloser) {
	buf, err := io.ReadAll(body)
	if err != nil {
		return "", body
	}
	_ = body.Close()

	bodyLog := string(buf)
	if len(bodyLog) > maxBodyLog {
		bodyLog = bodyLog[:maxBodyLog] + "..."
	}

	return strings.ReplaceAll(bodyLog, "\n", " "), io.NopCloser(bytes.NewReader(buf))
}

// headerLog renders headers for a log line, redacting the values of the ones that carry
// credentials - the same set SecretHeader keeps off foreign hosts.
func headerLog(h http.Header) string {
	safe := make(http.Header, len(h))
	for k, v := range h {
		if credentialHeader(k) {
			safe[k] = []string{"REDACTED"}
			continue
		}
		safe[k] = v
	}

	log, err := json.Marshal(safe)
	if err != nil {
		return fmt.Sprintf("%v", safe)
	}

	return string(log)
}

func wrapPrefix(prefix string) string {
	if prefix == "" {
		return ""
	}

	return prefix + " "
}
