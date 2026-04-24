// Package feishu provides a Feishu/Lark API client for document and drive operations.
package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const (
	baseURL  = "https://open.feishu.cn/open-apis"
	tokenURL = baseURL + "/auth/v3/tenant_access_token/internal"
	mediaType = "application/json; charset=utf-8"
)

// Client is a Feishu/Lark API client using tenant access token auth.
type Client struct {
	appID     string
	appSecret string
	token     string
	tokenMu   sync.RWMutex
	tokenExpiry time.Time
	httpClient *http.Client
}

// NewClient creates a Feishu client with the given app credentials.
func NewClient(appID, appSecret string) *Client {
	return &Client{
		appID:     appID,
		appSecret: appSecret,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// fetchToken obtains a new tenant access token.
func (c *Client) fetchToken(ctx context.Context) error {
	payload := map[string]string{
		"app_id":     c.appID,
		"app_secret": c.appSecret,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal token request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create token request: %w", err)
	}
	req.Header.Set("Content-Type", mediaType)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
		Expire           int    `json:"expire"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode token response: %w", err)
	}
	if result.Code != 0 {
		return fmt.Errorf("token error %d: %s", result.Code, result.Msg)
	}

	c.tokenMu.Lock()
	c.token = result.TenantAccessToken
	c.tokenExpiry = time.Now().Add(time.Duration(result.Expire-60) * time.Second)
	c.tokenMu.Unlock()
	return nil
}

// ensureToken refreshes the token if expired or missing.
func (c *Client) ensureToken(ctx context.Context) error {
	c.tokenMu.RLock()
	hasToken := c.token != "" && time.Now().Before(c.tokenExpiry)
	c.tokenMu.RUnlock()
	if hasToken {
		return nil
	}
	return c.fetchToken(ctx)
}

// do makes an authenticated GET or POST request to the given path.
func (c *Client) do(ctx context.Context, method, path string, queries url.Values, body interface{}) (json.RawMessage, error) {
	if err := c.ensureToken(ctx); err != nil {
		return nil, err
	}

	c.tokenMu.RLock()
	token := c.token
	c.tokenMu.RUnlock()

	reqURL := baseURL + path
	if queries != nil {
		reqURL += "?" + queries.Encode()
	}

	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, reqURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", mediaType)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var result struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("decode response: %w (body: %s)", err, string(respBody))
	}
	if result.Code != 0 {
		return nil, fmt.Errorf("API error %d: %s", result.Code, result.Msg)
	}
	return result.Data, nil
}

// ============================================================================
// Document API
// ============================================================================

// ReadDocument returns the raw content of a Feishu document.
func (c *Client) ReadDocument(ctx context.Context, documentID string) (string, error) {
	data, err := c.do(ctx, http.MethodGet,
		fmt.Sprintf("/docx/v1/documents/%s/raw_content", documentID),
		nil, nil)
	if err != nil {
		return "", err
	}
	var result struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", fmt.Errorf("parse document content: %w", err)
	}
	return result.Content, nil
}

// ============================================================================
// Drive/Comments API
// ============================================================================

// Comment represents a Feishu comment or reply.
type Comment struct {
	ID          string `json:"id"`
	ThreadID    string `json:"thread_id"`
	IsWhole     bool   `json:"is_whole"`
	CommentText string `json:"comment_text"`
	CreateTime  string `json:"create_time"`
	UpdateTime  string `json:"update_time"`
	Creator     string `json:"creator"`
}

// ListCommentsOptions specifies options for listing comments.
type ListCommentsOptions struct {
	FileToken string
	FileType  string
	IsWhole   bool
	PageSize  int
	PageToken string
}

// ListComments returns comments on a document.
func (c *Client) ListComments(ctx context.Context, opts ListCommentsOptions) ([]Comment, string, error) {
	if opts.FileType == "" {
		opts.FileType = "docx"
	}
	if opts.PageSize == 0 {
		opts.PageSize = 100
	}

	q := url.Values{}
	q.Set("file_type", opts.FileType)
	q.Set("user_id_type", "open_id")
	q.Set("page_size", fmt.Sprintf("%d", opts.PageSize))
	if opts.IsWhole {
		q.Set("is_whole", "true")
	}
	if opts.PageToken != "" {
		q.Set("page_token", opts.PageToken)
	}

	data, err := c.do(ctx, http.MethodGet,
		fmt.Sprintf("/drive/v1/files/%s/comments", opts.FileToken),
		q, nil)
	if err != nil {
		return nil, "", err
	}

	var result struct {
		Items     []Comment `json:"items"`
		PageToken string    `json:"page_token"`
		HasMore   bool      `json:"has_more"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, "", fmt.Errorf("parse comments: %w", err)
	}
	return result.Items, result.PageToken, nil
}

// ListReplies returns replies in a comment thread.
func (c *Client) ListReplies(ctx context.Context, fileToken, commentID, fileType string, pageSize int, pageToken string) ([]Comment, string, error) {
	if fileType == "" {
		fileType = "docx"
	}
	if pageSize == 0 {
		pageSize = 100
	}

	q := url.Values{}
	q.Set("file_type", fileType)
	q.Set("user_id_type", "open_id")
	q.Set("page_size", fmt.Sprintf("%d", pageSize))
	if pageToken != "" {
		q.Set("page_token", pageToken)
	}

	data, err := c.do(ctx, http.MethodGet,
		fmt.Sprintf("/drive/v1/files/%s/comments/%s/replies", fileToken, commentID),
		q, nil)
	if err != nil {
		return nil, "", err
	}

	var result struct {
		Items     []Comment `json:"items"`
		PageToken string    `json:"page_token"`
		HasMore   bool      `json:"has_more"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, "", fmt.Errorf("parse replies: %w", err)
	}
	return result.Items, result.PageToken, nil
}

// ReplyToComment posts a reply to an existing comment thread.
func (c *Client) ReplyToComment(ctx context.Context, fileToken, commentID, content, fileType string) error {
	if fileType == "" {
		fileType = "docx"
	}
	body := map[string]interface{}{
		"content": map[string]interface{}{
			"elements": []map[string]interface{}{
				{"type": "text_run", "text_run": map[string]interface{}{"text": content}},
			},
		},
	}
	q := url.Values{"file_type": []string{fileType}}

	_, err := c.do(ctx, http.MethodPost,
		fmt.Sprintf("/drive/v1/files/%s/comments/%s/replies", fileToken, commentID),
		q, body)
	return err
}

// AddComment posts a new whole-document comment.
func (c *Client) AddComment(ctx context.Context, fileToken, content, fileType string) error {
	if fileType == "" {
		fileType = "docx"
	}
	body := map[string]interface{}{
		"file_type": fileType,
		"reply_elements": []map[string]interface{}{
			{"type": "text", "text": content},
		},
	}
	_, err := c.do(ctx, http.MethodPost,
		fmt.Sprintf("/drive/v1/files/%s/new_comments", fileToken),
		nil, body)
	return err
}
