package outbound

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"math"
	rand "math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"time"

	"github.com/lihongjie0209/go-api-template/internal/config"
	"github.com/lihongjie0209/go-api-template/internal/idempotency"
	"github.com/lihongjie0209/go-api-template/internal/observability"
	"github.com/lihongjie0209/go-api-template/internal/requestid"
	"github.com/sony/gobreaker/v2"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

type HTTPClient struct {
	name    string
	baseURL *url.URL
	client  *http.Client
	cfg     config.HTTPUpstream
	breaker *gobreaker.CircuitBreaker[*http.Response]
	metrics *observability.Metrics
}

var validHTTPClientName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)

func (c *HTTPClient) CloseIdleConnections() { c.client.CloseIdleConnections() }

func NewHTTPClient(name string, cfg config.HTTPUpstream, metrics *observability.Metrics) (*HTTPClient, error) {
	if !validHTTPClientName.MatchString(name) {
		return nil, errors.New("outbound HTTP client name must be a bounded lowercase identifier")
	}
	baseURL, err := url.Parse(cfg.BaseURL)
	if err != nil || baseURL.Host == "" || (baseURL.Scheme != "http" && baseURL.Scheme != "https") || baseURL.User != nil || baseURL.RawQuery != "" || baseURL.Fragment != "" {
		return nil, errors.New("outbound HTTP base URL must be an http(s) endpoint without credentials, query, or fragment")
	}
	if cfg.Timeout < 10*time.Millisecond || cfg.Timeout > 5*time.Minute {
		return nil, errors.New("outbound HTTP timeout must be between 10ms and 5m")
	}
	if cfg.Retry.MaxAttempts < 1 || cfg.Retry.MaxAttempts > 5 || cfg.Retry.InitialBackoff < 10*time.Millisecond || cfg.Retry.InitialBackoff > time.Minute || cfg.Retry.MaxBackoff < cfg.Retry.InitialBackoff || cfg.Retry.MaxBackoff > time.Minute {
		return nil, errors.New("outbound HTTP retry policy is invalid")
	}
	if len(cfg.Retry.Methods) != 0 {
		return nil, errors.New("outbound HTTP retry methods are not configurable")
	}
	if cfg.Breaker.Enabled && (cfg.Breaker.FailureThreshold == 0 || cfg.Breaker.FailureThreshold > 10000 || cfg.Breaker.OpenTimeout < time.Second || cfg.Breaker.OpenTimeout > time.Hour) {
		return nil, errors.New("outbound HTTP breaker policy is invalid")
	}
	if err := validateHTTPClientTLS(cfg.TLS); err != nil {
		return nil, err
	}
	if cfg.Auth.Type != "" && cfg.Auth.Type != "bearer" && cfg.Auth.Type != "psk" {
		return nil, errors.New("outbound HTTP auth type must be bearer or psk")
	}
	if cfg.Auth.Type != "" && (cfg.Auth.Token == "" || len(cfg.Auth.Token) > 8192) {
		return nil, errors.New("outbound HTTP auth token is required and must not exceed 8192 bytes")
	}
	if cfg.Auth.Type == "" && cfg.Auth.Token != "" {
		return nil, errors.New("outbound HTTP auth type is required when a token is configured")
	}
	if cfg.TLS.Enabled && baseURL.Scheme != "https" {
		return nil, errors.New("outbound HTTP TLS requires an https base URL")
	}
	if cfg.Auth.Type != "" && baseURL.Scheme != "https" && !cfg.TLS.AllowInsecure {
		return nil, errors.New("refusing to send outbound HTTP credentials without TLS")
	}
	transport, err := httpTransport(cfg.TLS, cfg.Timeout)
	if err != nil {
		return nil, err
	}
	client := &HTTPClient{name: name, baseURL: baseURL, cfg: cfg, metrics: metrics, client: &http.Client{
		Transport: otelhttp.NewTransport(transport),
		Timeout:   cfg.Timeout,
		// Redirects are returned to the caller so credentials can never be
		// forwarded to a location selected by an upstream response.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	if cfg.Breaker.Enabled {
		client.breaker = gobreaker.NewCircuitBreaker[*http.Response](gobreaker.Settings{ //nolint:bodyclose // The caller owns every returned response body.
			Name: name, Timeout: cfg.Breaker.OpenTimeout,
			ReadyToTrip: func(counts gobreaker.Counts) bool { return counts.ConsecutiveFailures >= cfg.Breaker.FailureThreshold },
			IsExcluded:  func(err error) bool { return errors.Is(err, context.Canceled) },
		})
	}
	return client, nil
}

func (c *HTTPClient) Do(ctx context.Context, method, requestPath string, body []byte, headers http.Header) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	started := time.Now()
	response, err := c.execute(ctx, method, requestPath, body, headers)
	if response == nil {
		cancel()
	} else {
		response.Body = &cancelOnCloseBody{ReadCloser: response.Body, cancel: cancel}
	}
	status := "error"
	if response != nil {
		status = strconv.Itoa(response.StatusCode)
	}
	if c.metrics != nil && c.metrics.Enabled() {
		c.metrics.OutboundRequests.WithLabelValues("http", c.name, status).Inc()
		c.metrics.OutboundDuration.WithLabelValues("http", c.name).Observe(time.Since(started).Seconds())
	}
	return response, err
}

type cancelOnCloseBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelOnCloseBody) Read(buffer []byte) (int, error) {
	read, err := b.ReadCloser.Read(buffer)
	if err != nil {
		b.cancel()
	}
	return read, err
}

func (b *cancelOnCloseBody) Close() error {
	defer b.cancel()
	return b.ReadCloser.Close()
}

func (c *HTTPClient) execute(ctx context.Context, method, requestPath string, body []byte, headers http.Header) (*http.Response, error) {
	if c.breaker == nil {
		return c.doWithRetry(ctx, method, requestPath, body, headers)
	}
	call := func() (*http.Response, error) {
		response, err := c.doWithRetry(ctx, method, requestPath, body, headers)
		if err == nil && response != nil && retryableHTTPStatus(response.StatusCode) {
			return nil, &statusError{response: response}
		}
		return response, err
	}
	response, err := c.breaker.Execute(call)
	var statusErr *statusError
	if errors.As(err, &statusErr) {
		return statusErr.response, nil
	}
	return response, err
}

func (c *HTTPClient) doWithRetry(ctx context.Context, method, requestPath string, body []byte, headers http.Header) (*http.Response, error) {
	reference, err := url.Parse(requestPath)
	if err != nil || reference.IsAbs() || reference.Host != "" {
		return nil, errors.New("outbound HTTP path must be relative")
	}
	target := c.baseURL.ResolveReference(reference).String()
	maxAttempts := 1
	if retryableMethod(ctx, method) {
		maxAttempts = c.cfg.Retry.MaxAttempts
	}
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		request, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("create outbound HTTP request: %w", err)
		}
		request.Header = headers.Clone()
		if request.Header == nil {
			request.Header = make(http.Header)
		}
		if len(body) > 0 && request.Header.Get("Content-Type") == "" {
			request.Header.Set("Content-Type", "application/json")
		}
		if id, ok := requestid.FromContext(ctx); ok {
			request.Header.Set("X-Request-ID", id)
		}
		if key, ok := idempotency.FromContext(ctx); ok {
			request.Header.Set("Idempotency-Key", key)
		}
		applyHTTPAuth(request, c.cfg.Auth)
		response, err := c.client.Do(request)
		if err == nil && (response == nil || !retryableHTTPStatus(response.StatusCode) || attempt == maxAttempts) {
			return response, nil
		}
		if err != nil && attempt == maxAttempts {
			return nil, fmt.Errorf("call outbound HTTP service: %w", err)
		}
		if response != nil {
			_, _ = io.CopyN(io.Discard, response.Body, 32<<10)
			_ = response.Body.Close()
		}
		if attempt < maxAttempts {
			if err := waitBackoff(ctx, c.cfg.Retry, attempt); err != nil {
				return nil, err
			}
		}
	}
	return nil, errors.New("outbound HTTP attempts exhausted")
}

func retryableMethod(ctx context.Context, method string) bool {
	if method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions || method == http.MethodPut || method == http.MethodDelete {
		return true
	}
	_, ok := idempotency.FromContext(ctx)
	return ok
}
func retryableHTTPStatus(status int) bool {
	return status == 429 || status == 502 || status == 503 || status == 504
}
func applyHTTPAuth(request *http.Request, auth config.ClientAuth) {
	switch auth.Type {
	case "bearer":
		request.Header.Set("Authorization", "Bearer "+auth.Token)
	case "psk":
		request.Header.Set("Authorization", "PSK "+auth.Token)
	}
}
func waitBackoff(ctx context.Context, retry config.Retry, attempt int) error {
	delay := time.Duration(float64(retry.InitialBackoff) * math.Pow(2, float64(attempt-1)))
	if delay > retry.MaxBackoff {
		delay = retry.MaxBackoff
	}
	// Equal jitter preserves exponential growth while preventing synchronized
	// retries across replicas after a shared upstream failure.
	half := delay / 2
	if spread := delay - half; spread > 0 {
		delay = half + time.Duration(rand.Int64N(int64(spread)+1))
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type statusError struct{ response *http.Response }

func (e *statusError) Error() string { return "retryable upstream HTTP status " + e.response.Status }

func httpTransport(cfg config.ClientTLS, timeout time.Duration) (*http.Transport, error) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: cfg.ServerName}
	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read outbound HTTP CA: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			return nil, fmt.Errorf("load system certificate pool: %w", err)
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("parse outbound HTTP CA")
		}
		tlsConfig.RootCAs = pool
	}
	if cfg.CertFile != "" {
		certificate, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("load outbound HTTP client certificate: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{certificate}
	}
	return &http.Transport{Proxy: http.ProxyFromEnvironment, TLSClientConfig: tlsConfig, ForceAttemptHTTP2: true, MaxIdleConns: 100, MaxIdleConnsPerHost: 10, IdleConnTimeout: 90 * time.Second, ResponseHeaderTimeout: timeout}, nil
}

func validateHTTPClientTLS(cfg config.ClientTLS) error {
	if !cfg.Enabled && (cfg.ServerName != "" || cfg.CAFile != "" || cfg.CertFile != "" || cfg.KeyFile != "") {
		return errors.New("outbound HTTP TLS settings require tls.enabled")
	}
	if (cfg.CertFile == "") != (cfg.KeyFile == "") {
		return errors.New("outbound HTTP client certificate and key must be configured together")
	}
	if cfg.Enabled && cfg.AllowInsecure {
		return errors.New("outbound HTTP TLS and allow_insecure are mutually exclusive")
	}
	if len(cfg.ServerName) > 253 || len(cfg.CAFile) > 4096 || len(cfg.CertFile) > 4096 || len(cfg.KeyFile) > 4096 {
		return errors.New("outbound HTTP TLS settings exceed their bounds")
	}
	return nil
}
