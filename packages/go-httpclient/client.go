// Package httpclient provides a production-tuned *http.Client factory for
// all H2Fleet service-to-service and service-to-middleware HTTP calls.
//
// Wave-11 performance: Go's http.DefaultTransport caps MaxIdleConnsPerHost at
// 2, so any burst of concurrent calls to a single upstream (Keycloak, APISIX,
// OpenSearch, Mojaloop, toggle-service, webhooks) churns fresh TCP+TLS
// handshakes — the dominant source of p99 jitter on the hot paths. Pooling a
// per-host idle cache sized for burst concurrency keeps handshakes off the
// request path and holds tail latency flat.
package httpclient

import (
	"net"
	"net/http"
	"time"
)

// New returns an *http.Client with the given overall timeout and a tuned
// Transport. A timeout <= 0 leaves the client without an overall deadline
// (callers that stream long responses may opt out deliberately).
func New(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: Transport(),
	}
}

// Transport returns the shared-tuning Transport. Exported so callers that
// manage retries/backoff at the RoundTripper level can wrap it.
func Transport() *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   2 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   3 * time.Second,
		ResponseHeaderTimeout: 0, // per-request deadlines live on the Client
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
	}
}
