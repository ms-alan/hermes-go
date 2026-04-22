package model

import (
	"errors"
	"fmt"
)

// Sentinel errors for the model package.
var (
	ErrInvalidRequest  = errors.New("invalid request")
	ErrNoBaseURL       = errors.New("base URL is required")
	ErrMissingAPIKey   = errors.New("API key is required")
	ErrMissingModel    = errors.New("model name is required")
	ErrUnsupportedRole = errors.New("unsupported role")
	ErrMissingToolName = errors.New("tool name is missing")
	ErrMissingToolCallID = errors.New("tool call ID is missing")
	ErrUnexpectedEOF   = errors.New("unexpected end of response")
	ErrTimeout         = errors.New("request timeout")
)

// RequestError wraps an HTTP error with context.
type RequestError struct {
	StatusCode int
	Message    string
	Raw        error
}

func (e *RequestError) Error() string {
	return fmt.Sprintf("request failed (status %d): %s", e.StatusCode, e.Message)
}

func (e *RequestError) Unwrap() error {
	return e.Raw
}

// APIError represents an error returned by the LLM API.
type APIError struct {
	Code    int    `json:"code,omitempty"`
	Message string `json:"message"`
	Type    string `json:"type,omitempty"`
}

func (e *APIError) Error() string {
	if e.Type != "" {
		return fmt.Sprintf("api error [%s]: %s", e.Type, e.Message)
	}
	return fmt.Sprintf("api error: %s", e.Message)
}

// ErrUnexpectedStatus returns an APIError for non-200 responses.
func ErrUnexpectedStatus(status int, body []byte) *APIError {
	msg := string(body)
	if len(msg) > 200 {
		msg = msg[:200] + "..."
	}
	return &APIError{Code: status, Message: msg}
}
