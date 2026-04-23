package qqbot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// APIClient handles QQBot REST API calls (api.sgroup.qq.com).
type APIClient struct {
	AppID       string
	Token       string // "Bot <appid>.<token>"
	AccessToken string
	HTTP        *http.Client
	BaseURL     string
}

// NewAPIClient creates an APIClient and obtains a bot token.
func NewAPIClient(appID, appSecret string) (*APIClient, error) {
	c := &APIClient{
		AppID:   appID,
		Token:   "Bot " + appID,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
		BaseURL: "https://api.sgroup.qq.com",
	}
	if err := c.fetchToken(context.Background(), appSecret); err != nil {
		return nil, fmt.Errorf("qqbot token: %w", err)
	}
	return c, nil
}

func (c *APIClient) fetchToken(ctx context.Context, appSecret string) error {
	// Note: AppID and appSecret are numeric/alphanumeric from QQ Bot config
	// and are not expected to contain URL-special characters. For stricter
	// handling, consider net/url encoding if QQ changes credential format.
	url := "https://login.q.qq.com/getToken?grant_type=client_credentials&client_id=" + c.AppID + "&client_secret=" + appSecret
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("create token request: %w", err)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("fetch token request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read token response: %w", err)
	}
	var result struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("parse token: %w", err)
	}
	c.AccessToken = result.AccessToken
	c.Token = "Bot " + c.AppID + "." + result.AccessToken
	return nil
}

// GetGatewayURL returns the WebSocket gateway URL.
func (c *APIClient) GetGatewayURL(ctx context.Context) (string, error) {
	url := c.BaseURL + "/gateway/bot"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", c.Token)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("get gateway: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read gateway response: %w", err)
	}
	var result struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("parse gateway: %w", err)
	}
	return result.URL, nil
}

func (c *APIClient) doRequest(ctx context.Context, method, url string, body any) (*APIResponse, error) {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", c.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request %s %s: %w", method, url, err)
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	var result APIResponse
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return nil, fmt.Errorf("parse API response: %w", err)
	}
	if result.Code != 0 {
		return nil, fmt.Errorf("QQ API error %d: %s", result.Code, result.Msg)
	}
	return &result, nil
}

// SendMessage sends a text message to a channel.
func (c *APIClient) SendMessage(ctx context.Context, channelID, content string) (*SendMessageResponse, error) {
	url := c.BaseURL + "/channels/" + channelID + "/messages"
	body := map[string]any{
		"content": content,
		"msg_id":  time.Now().UnixMilli(),
	}
	resp, err := c.doRequest(ctx, http.MethodPost, url, body)
	if err != nil {
		return nil, err
	}
	if resp.Data == nil {
		return nil, fmt.Errorf("no data in send message response")
	}
	var result SendMessageResponse
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		return nil, fmt.Errorf("parse send response: %w", err)
	}
	return &result, nil
}

// SendMarkdown sends a markdown message (msg_type=2).
func (c *APIClient) SendMarkdown(ctx context.Context, channelID, markdown string) error {
	url := c.BaseURL + "/channels/" + channelID + "/messages"
	body := map[string]any{
		"msg_id":   time.Now().UnixMilli(),
		"msg_type": 2,
		"content":  map[string]any{"template_id": markdown},
	}
	_, err := c.doRequest(ctx, http.MethodPost, url, body)
	return err
}

// UploadMedia uploads a local file and returns the URL.
func (c *APIClient) UploadMedia(ctx context.Context, channelID, filePath, fileType string) (string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}
	buf := &bytes.Buffer{}
	writer := multipart.NewWriter(buf)
	part, err := writer.CreateFormFile("file", filepath.Base(filePath))
	if err != nil {
		return "", fmt.Errorf("create form file: %w", err)
	}
	if _, err := part.Write(data); err != nil {
		return "", fmt.Errorf("write file data: %w", err)
	}
	writer.WriteField("msg_id", strconv.FormatInt(time.Now().UnixMilli(), 10))
	writer.WriteField("file_type", mapFileType(fileType))
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("close writer: %w", err)
	}
	url := c.BaseURL + "/channels/" + channelID + "/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", c.Token)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("upload request: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read upload response: %w", err)
	}
	var result APIResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("parse upload response: %w", err)
	}
	if result.Code != 0 {
		return "", fmt.Errorf("QQ API error %d: %s", result.Code, result.Msg)
	}
	var uploadResp UploadMediaResponse
	if err := json.Unmarshal(result.Data, &uploadResp); err != nil {
		return "", fmt.Errorf("parse upload data: %w", err)
	}
	return uploadResp.URL, nil
}

func mapFileType(fileType string) string {
	switch fileType {
	case "image":
		return "1"
	case "audio":
		return "2"
	case "video":
		return "3"
	default:
		return "8"
	}
}
