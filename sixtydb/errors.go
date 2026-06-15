package sixtydb

import "fmt"

// APIError represents an error response from the 60db API.
//
// The 60db REST endpoints return JSON error payloads. Where the body cannot be
// decoded into this shape, only StatusCode and Message (the raw body) are
// populated.
type APIError struct {
	// StatusCode is the HTTP status code returned by the server. It is 0 for
	// errors surfaced from the WebSocket or streaming "error" frames.
	StatusCode int `json:"-"`
	// Success mirrors the "success" field returned by the API (always false for
	// an error).
	Success bool `json:"success"`
	// Message is a human-readable description of the error.
	Message string `json:"message"`
	// Error is an alternative field name some endpoints use for the error text.
	Error_ string `json:"error,omitempty"`
}

func (e *APIError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = e.Error_
	}
	if e.StatusCode != 0 {
		return fmt.Sprintf("60db api error (status %d) - %s", e.StatusCode, msg)
	}
	return fmt.Sprintf("60db api error - %s", msg)
}
