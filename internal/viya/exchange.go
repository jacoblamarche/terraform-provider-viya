package viya

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	maxResponseBytes      = 10 << 20
	defaultRequestTimeout = 60 * time.Second
)

// LogFunc receives one event per HTTP exchange. It has the signature of
// tflog.Debug, so that the provider can pass that function as is.
type LogFunc func(ctx context.Context, message string, fields ...map[string]any)

// errNoResponse marks an exchange that was abandoned at its time limit.
var errNoResponse = errors.New("no response from SAS Viya")

// exchanger performs HTTP exchanges with a time limit, and logs each one.
type exchanger struct {
	timeout time.Duration
	log     LogFunc
}

func newExchanger(timeout time.Duration, log LogFunc) exchanger {
	if timeout <= 0 {
		timeout = defaultRequestTimeout
	}
	return exchanger{timeout: timeout, log: log}
}

// do sends a request and reads the whole response.
//
// The log event holds the method, the path, the status and the duration.
// Bodies and headers are left out on purpose: they carry tokens, and SAS
// Viya returns passwords in clear text.
func (e exchanger) do(ctx context.Context, req *http.Request) (int, http.Header, []byte, error) {
	limited, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	start := time.Now()
	status, header, body, err := roundTrip(req.WithContext(limited))
	if err != nil && ctx.Err() == nil && limited.Err() != nil {
		err = fmt.Errorf("%w within %s", errNoResponse, e.timeout)
	}

	if e.log != nil {
		fields := map[string]any{
			"method":      req.Method,
			"path":        req.URL.Path,
			"duration_ms": time.Since(start).Milliseconds(),
		}
		if err != nil {
			fields["error"] = err.Error()
		} else {
			fields["status"] = status
		}
		e.log(ctx, "SAS Viya API request", fields)
	}
	return status, header, body, err
}

func roundTrip(req *http.Request) (int, http.Header, []byte, error) {
	// The request targets the endpoint that the operator configured, which
	// is the purpose of the client rather than a forged destination.
	resp, err := http.DefaultClient.Do(req) //nolint:gosec
	if err != nil {
		return 0, nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return 0, nil, nil, fmt.Errorf("reading response: %w", err)
	}
	return resp.StatusCode, resp.Header, body, nil
}
