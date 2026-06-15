package sixtydb

import (
	"context"
	"time"
)

// NewTestClient returns a Client with its REST base URL and WebSocket URL
// overridden, for pointing tests at an httptest server. It mirrors the
// elevenlabs package's NewMockClient helper.
func NewTestClient(ctx context.Context, baseURL, wsURL, apiKey string, reqTimeout time.Duration) *Client {
	c := NewClient(ctx, apiKey, reqTimeout)
	c.baseURL = baseURL
	c.wsURL = wsURL
	return c
}
