package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

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

func WithRequest() LoggerOption {
	return func(d *loggerConfig) {
		d.logReq = true
	}
}

func WithHeader() LoggerOption {
	return func(d *loggerConfig) {
		d.logHeaders = true
	}
}

func WithResponse(f LogFunc) LoggerOption {
	return func(d *loggerConfig) {
		d.logResp = true
		d.respLogFunc = f
	}
}

func WithError(f LogFunc) LoggerOption {
	return func(d *loggerConfig) {
		d.logErr = true
		d.errLogFunc = f
	}
}

func WithPrefix(prefix string) LoggerOption {
	return func(d *loggerConfig) {
		d.prefix = prefix
	}
}

func WithLogArgs(args ...any) LoggerOption {
	return func(d *loggerConfig) {
		d.args = args
	}
}

func DiscardLoggerFunc(_ context.Context, _ string, _ ...any) { return }

func Logger(opts ...LoggerOption) func(http.RoundTripper) http.RoundTripper {
	cfg := newDefaulLoggerConfig()
	for _, o := range opts {
		o(cfg)
	}

	return func(next http.RoundTripper) http.RoundTripper {
		return RoundTripperFunc(func(r *http.Request) (*http.Response, error) {
			st := time.Now()

			logMessage := fmt.Sprintf("%sclient", wrapPrefix(cfg.prefix))
			logParts := []any{"method", r.Method, "target", r.URL}

			{
				reqLogParts := []any{logParts}
				if cfg.logHeaders && len(r.Header) > 0 {
					headerLog, err := json.Marshal(r.Header)
					if err != nil {
						headerLog = fmt.Appendf(nil, "%v", r.Header)
					}
					reqLogParts = append(reqLogParts, "headers", headerLog)
				}

				if cfg.logReq && r.Body != nil {
					var bodyLog string
					body, e := io.ReadAll(r.Body)
					if e == nil {
						_ = r.Body.Close()
						r.Body = io.NopCloser(bytes.NewReader(body))
						bodyLog = string(body)
						if len(bodyLog) > 1024 {
							bodyLog = bodyLog[:1024] + "..."
						}
						bodyLog = strings.ReplaceAll(bodyLog, "\n", " ")
					}
					if bodyLog != "" {
						reqLogParts = append(reqLogParts, "request body", bodyLog)
					}
				}
				cfg.logFunc(r.Context(), logMessage, reqLogParts...)
			}

			if !cfg.logResp && !cfg.logErr {
				return next.RoundTrip(r)
			}

			respLogParts := []any{logParts}
			resp, err := next.RoundTrip(r)
			respLogParts = append(respLogParts, "duration", time.Since(st).String())
			if err != nil && cfg.logErr {
				cfg.errLogFunc(r.Context(), logMessage, "error", err.Error())
			}
			respLogParts = append(respLogParts, "code", resp.StatusCode)

			if err != nil && resp.Body != nil {
				var bodyLog string
				body, e := io.ReadAll(resp.Body)
				if e == nil {
					_ = resp.Body.Close()
					resp.Body = io.NopCloser(bytes.NewReader(body))
					bodyLog = string(body)
					if len(bodyLog) > 1024 {
						bodyLog = bodyLog[:1024] + "..."
					}
					bodyLog = strings.ReplaceAll(bodyLog, "\n", " ")
				}
				if bodyLog != "" {
					respLogParts = append(respLogParts, "response body", bodyLog)
				}
			}

			cfg.respLogFunc(r.Context(), logMessage, respLogParts...)
			return resp, err
		})
	}
}

func wrapPrefix(prefix string) string {
	if prefix == "" {
		return ""
	}

	return prefix + " "
}
