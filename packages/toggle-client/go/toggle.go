// Package toggle is the Go client SDK for the H2Fleet feature-toggle service.
//
// Contract (SPEC §3.2):
//   - isEnabled(module) -> bool
//   - 5s local cache per module
//   - fail-closed: any error (network, non-200, malformed body) => disabled (false)
//
// The SDK talks to the toggle-service REST API:
//
//	GET /v1/toggles/{module} -> { "module": "<id>", "enabled": true, "domain": "<domain>" }
package toggle

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// DefaultTTL is the local cache TTL mandated by SPEC §3.2.
const DefaultTTL = 5 * time.Second

// toggleResponse mirrors the toggle-service GET /v1/toggles/{module} payload.
type toggleResponse struct {
	Module  string `json:"module"`
	Enabled bool   `json:"enabled"`
	Domain  string `json:"domain"`
}

type cacheEntry struct {
	enabled   bool
	expiresAt time.Time
}

// Client is a concurrency-safe feature-toggle client with a per-module TTL cache.
type Client struct {
	baseURL string
	http    *http.Client
	ttl     time.Duration

	mu    sync.Mutex
	cache map[string]cacheEntry
}

// New creates a Client pointing at the toggle-service base URL (e.g. "http://toggle-service:8080").
// The local cache TTL is 5s per SPEC §3.2.
func New(url string) *Client {
	return &Client{
		baseURL: url,
		http:    &http.Client{Timeout: 3 * time.Second},
		ttl:     DefaultTTL,
		cache:   make(map[string]cacheEntry),
	}
}

// IsEnabled reports whether the given module toggle is enabled.
// It serves from the 5s local cache when fresh; otherwise it queries the
// toggle-service. It is fail-closed: any error results in false, and failed
// lookups are cached negatively for the TTL to avoid hammering a degraded
// toggle-service.
func (c *Client) IsEnabled(ctx context.Context, module string) bool {
	now := time.Now()

	c.mu.Lock()
	if e, ok := c.cache[module]; ok && now.Before(e.expiresAt) {
		c.mu.Unlock()
		return e.enabled
	}
	c.mu.Unlock()

	enabled := c.fetch(ctx, module)

	c.mu.Lock()
	c.cache[module] = cacheEntry{enabled: enabled, expiresAt: time.Now().Add(c.ttl)}
	c.mu.Unlock()

	return enabled
}

// fetch queries GET /v1/toggles/{module}. Fail-closed: false on any error.
func (c *Client) fetch(ctx context.Context, module string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/toggles/"+module, nil)
	if err != nil {
		return false
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false
	}

	var tr toggleResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return false
	}
	if tr.Module != "" && tr.Module != module {
		return false
	}
	return tr.Enabled
}

// Invalidate drops a cached entry (e.g. after consuming a toggle.changed event).
func (c *Client) Invalidate(module string) {
	c.mu.Lock()
	delete(c.cache, module)
	c.mu.Unlock()
}

// String describes the client for logging.
func (c *Client) String() string {
	return fmt.Sprintf("toggle.Client{url=%s, ttl=%s}", c.baseURL, c.ttl)
}
