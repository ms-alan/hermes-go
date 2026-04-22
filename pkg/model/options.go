package model

import (
	"net/http"
)

// Option is a functional option for configuring the LLM client.
type Option func(*options)

type options struct {
	baseURL      string
	apiKey       string
	model        string
	httpClient   *http.Client
	extraHeaders map[string]string
	timeoutSecs  int
}

// WithBaseURL sets the API base URL.
func WithBaseURL(url string) Option {
	return func(o *options) { o.baseURL = url }
}

// WithAPIKey sets the API key.
func WithAPIKey(key string) Option {
	return func(o *options) { o.apiKey = key }
}

// WithModel sets the model name.
func WithModel(model string) Option {
	return func(o *options) { o.model = model }
}

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(cli *http.Client) Option {
	return func(o *options) { o.httpClient = cli }
}

// WithExtraHeaders sets additional headers on every request.
func WithExtraHeaders(headers map[string]string) Option {
	return func(o *options) { o.extraHeaders = headers }
}

// WithTimeout sets the request timeout in seconds.
func WithTimeout(secs int) Option {
	return func(o *options) { o.timeoutSecs = secs }
}

// ApplyOptions applies the given options to a options struct.
func (o *options) apply(opts ...Option) {
	for _, opt := range opts {
		opt(o)
	}
}
