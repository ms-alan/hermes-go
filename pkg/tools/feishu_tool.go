package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/nousresearch/hermes-go/pkg/feishu"
)

// ============================================================================
// Feishu/Lark Tools
// ============================================================================
//
// Tools for reading Feishu documents and managing document comments.
// Requires FEISHU_APP_ID and FEISHU_APP_SECRET environment variables.
//
// Document token format: https://[domain].feishu.cn/docx/[DOC_TOKEN]
// File token: the [DOC_TOKEN] part of the URL.

// feishuClient is the global Feishu client, lazily initialized.
var (
	feishuClient   *feishu.Client
	feishuClientMu sync.RWMutex
)

// getFeishuClient returns the global Feishu client, initializing it on first call.
func getFeishuClient() (*feishu.Client, error) {
	feishuClientMu.RLock()
	if feishuClient != nil {
		feishuClientMu.RUnlock()
		return feishuClient, nil
	}
	feishuClientMu.RUnlock()

	feishuClientMu.Lock()
	defer feishuClientMu.Unlock()
	if feishuClient != nil {
		return feishuClient, nil
	}

	appID := os.Getenv("FEISHU_APP_ID")
	appSecret := os.Getenv("FEISHU_APP_SECRET")
	if appID == "" || appSecret == "" {
		return nil, fmt.Errorf("FEISHU_APP_ID and FEISHU_APP_SECRET must be set")
	}
	feishuClient = feishu.NewClient(appID, appSecret)
	return feishuClient, nil
}

// feishuCheck returns true if Feishu credentials are available.
func feishuCheck() bool {
	_, err := getFeishuClient()
	return err == nil
}

// ============================================================================
// Tool schemas
// ============================================================================

var feishuDocReadSchema = map[string]any{
	"description": "Read the full content of a Feishu/Lark document as plain text. Extract the document token from the URL: https://[domain].feishu.cn/docx/[DOC_TOKEN].",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"doc_token": map[string]any{
				"type":        "string",
				"description": "The document token extracted from the Feishu document URL.",
			},
		},
		"required": []string{"doc_token"},
	},
}

var feishuDriveListCommentsSchema = map[string]any{
	"description": "List comments on a Feishu/Lark document. Use is_whole=true to list whole-document comments only.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"file_token": map[string]any{"type": "string", "description": "The document file token."},
			"file_type":  map[string]any{"type": "string", "description": "File type (default: docx).", "default": "docx"},
			"is_whole":   map[string]any{"type": "boolean", "description": "If true, only return whole-document comments.", "default": false},
			"page_size":  map[string]any{"type": "integer", "description": "Number of comments per page (max 100).", "default": 100},
			"page_token": map[string]any{"type": "string", "description": "Pagination token for next page."},
		},
		"required": []string{"file_token"},
	},
}

var feishuDriveListRepliesSchema = map[string]any{
	"description": "List all replies in a comment thread on a Feishu document.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"file_token":  map[string]any{"type": "string", "description": "The document file token."},
			"comment_id":  map[string]any{"type": "string", "description": "The comment ID to list replies for."},
			"file_type":   map[string]any{"type": "string", "description": "File type (default: docx).", "default": "docx"},
			"page_size":   map[string]any{"type": "integer", "description": "Number of replies per page (max 100).", "default": 100},
			"page_token":  map[string]any{"type": "string", "description": "Pagination token for next page."},
		},
		"required": []string{"file_token", "comment_id"},
	},
}

var feishuDriveReplySchema = map[string]any{
	"description": "Reply to a local (quoted-text) comment thread on a Feishu document. For whole-document comments, use feishu_drive_add_comment instead.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"file_token": map[string]any{"type": "string", "description": "The document file token."},
			"comment_id": map[string]any{"type": "string", "description": "The comment ID to reply to."},
			"content":    map[string]any{"type": "string", "description": "The reply text content (plain text only, no markdown)."},
			"file_type": map[string]any{"type": "string", "description": "File type (default: docx).", "default": "docx"},
		},
		"required": []string{"file_token", "comment_id", "content"},
	},
}

var feishuDriveAddCommentSchema = map[string]any{
	"description": "Add a new whole-document comment on a Feishu document.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"file_token": map[string]any{"type": "string", "description": "The document file token."},
			"content":    map[string]any{"type": "string", "description": "The comment text content (plain text only, no markdown)."},
			"file_type": map[string]any{"type": "string", "description": "File type (default: docx).", "default": "docx"},
		},
		"required": []string{"file_token", "content"},
	},
}

// ============================================================================
// Tool handlers
// ============================================================================

func feishuDocReadHandler(args map[string]interface{}) string {
	docToken := strings.TrimSpace(feishuStr(args, "doc_token"))
	if docToken == "" {
		return toolError("doc_token is required")
	}

	client, err := getFeishuClient()
	if err != nil {
		return toolError("Feishu client not available: " + err.Error())
	}

	content, err := client.ReadDocument(context.Background(), docToken)
	if err != nil {
		return toolError("Failed to read document: " + err.Error())
	}

	return toolResultData(map[string]interface{}{
		"success": true,
		"content": content,
	})
}

func feishuDriveListCommentsHandler(args map[string]interface{}) string {
	client, err := getFeishuClient()
	if err != nil {
		return toolError("Feishu client not available: " + err.Error())
	}

	fileToken := strings.TrimSpace(feishuStr(args, "file_token"))
	if fileToken == "" {
		return toolError("file_token is required")
	}

	opts := feishu.ListCommentsOptions{
		FileToken: fileToken,
		FileType:  feishuStrDef(args, "file_type", "docx"),
		IsWhole:   feishuBool(args, "is_whole"),
		PageSize:  feishuInt(args, "page_size", 100),
		PageToken: feishuStr(args, "page_token"),
	}

	comments, pageToken, err := client.ListComments(context.Background(), opts)
	if err != nil {
		return toolError("List comments failed: " + err.Error())
	}

	type CommentOut struct {
		ID          string `json:"id"`
		ThreadID    string `json:"thread_id"`
		IsWhole     bool   `json:"is_whole"`
		CommentText string `json:"comment_text"`
		CreateTime  string `json:"create_time"`
		Creator     string `json:"creator"`
	}
	out := make([]CommentOut, len(comments))
	for i, c := range comments {
		out[i] = CommentOut{ID: c.ID, ThreadID: c.ThreadID, IsWhole: c.IsWhole, CommentText: c.CommentText, CreateTime: c.CreateTime, Creator: c.Creator}
	}
	return toolResultData(map[string]interface{}{
		"success":    true,
		"comments":   out,
		"page_token": pageToken,
	})
}

func feishuDriveListRepliesHandler(args map[string]interface{}) string {
	client, err := getFeishuClient()
	if err != nil {
		return toolError("Feishu client not available: " + err.Error())
	}

	fileToken := strings.TrimSpace(feishuStr(args, "file_token"))
	commentID := strings.TrimSpace(feishuStr(args, "comment_id"))
	if fileToken == "" || commentID == "" {
		return toolError("file_token and comment_id are required")
	}

	replies, pageToken, err := client.ListReplies(
		context.Background(),
		fileToken,
		commentID,
		feishuStrDef(args, "file_type", "docx"),
		feishuInt(args, "page_size", 100),
		feishuStr(args, "page_token"),
	)
	if err != nil {
		return toolError("List replies failed: " + err.Error())
	}

	type ReplyOut struct {
		ID          string `json:"id"`
		ThreadID    string `json:"thread_id"`
		IsWhole     bool   `json:"is_whole"`
		CommentText string `json:"comment_text"`
		CreateTime  string `json:"create_time"`
		Creator     string `json:"creator"`
	}
	out := make([]ReplyOut, len(replies))
	for i, r := range replies {
		out[i] = ReplyOut{ID: r.ID, ThreadID: r.ThreadID, IsWhole: r.IsWhole, CommentText: r.CommentText, CreateTime: r.CreateTime, Creator: r.Creator}
	}
	return toolResultData(map[string]interface{}{
		"success":    true,
		"replies":    out,
		"page_token": pageToken,
	})
}

func feishuDriveReplyHandler(args map[string]interface{}) string {
	client, err := getFeishuClient()
	if err != nil {
		return toolError("Feishu client not available: " + err.Error())
	}

	fileToken := strings.TrimSpace(feishuStr(args, "file_token"))
	commentID := strings.TrimSpace(feishuStr(args, "comment_id"))
	content := strings.TrimSpace(feishuStr(args, "content"))
	if fileToken == "" || commentID == "" || content == "" {
		return toolError("file_token, comment_id, and content are required")
	}

	err = client.ReplyToComment(context.Background(), fileToken, commentID, content, feishuStrDef(args, "file_type", "docx"))
	if err != nil {
		return toolError("Reply comment failed: " + err.Error())
	}
	return toolResultData(map[string]interface{}{"success": true})
}

func feishuDriveAddCommentHandler(args map[string]interface{}) string {
	client, err := getFeishuClient()
	if err != nil {
		return toolError("Feishu client not available: " + err.Error())
	}

	fileToken := strings.TrimSpace(feishuStr(args, "file_token"))
	content := strings.TrimSpace(feishuStr(args, "content"))
	if fileToken == "" || content == "" {
		return toolError("file_token and content are required")
	}

	err = client.AddComment(context.Background(), fileToken, content, feishuStrDef(args, "file_type", "docx"))
	if err != nil {
		return toolError("Add comment failed: " + err.Error())
	}
	return toolResultData(map[string]interface{}{"success": true})
}

// ============================================================================
// Helpers (avoid name collision with send_message_tool.go)
// ============================================================================

func feishuStr(args map[string]interface{}, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

func feishuStrDef(args map[string]interface{}, key, defaultVal string) string {
	if v, ok := args[key].(string); ok && v != "" {
		return v
	}
	return defaultVal
}

func feishuBool(args map[string]interface{}, key string) bool {
	if v, ok := args[key].(bool); ok {
		return v
	}
	return false
}

func feishuInt(args map[string]interface{}, key string, defaultVal int) int {
	if v, ok := args[key].(float64); ok {
		return int(v)
	}
	if v, ok := args[key].(int); ok {
		return v
	}
	return defaultVal
}

// ============================================================================
// Init
// ============================================================================

func init() {
	Register("feishu_doc_read", "feishu_doc", feishuDocReadSchema, feishuDocReadHandler, feishuCheck,
		[]string{"FEISHU_APP_ID", "FEISHU_APP_SECRET"}, false,
		"Read Feishu document content as plain text", "📄")

	Register("feishu_drive_list_comments", "feishu_drive", feishuDriveListCommentsSchema, feishuDriveListCommentsHandler, feishuCheck,
		[]string{"FEISHU_APP_ID", "FEISHU_APP_SECRET"}, false,
		"List comments on a Feishu document", "💬")

	Register("feishu_drive_list_comment_replies", "feishu_drive", feishuDriveListRepliesSchema, feishuDriveListRepliesHandler, feishuCheck,
		[]string{"FEISHU_APP_ID", "FEISHU_APP_SECRET"}, false,
		"List replies in a Feishu document comment thread", "🔁")

	Register("feishu_drive_reply_comment", "feishu_drive", feishuDriveReplySchema, feishuDriveReplyHandler, feishuCheck,
		[]string{"FEISHU_APP_ID", "FEISHU_APP_SECRET"}, false,
		"Reply to a Feishu document comment thread", "↩️")

	Register("feishu_drive_add_comment", "feishu_drive", feishuDriveAddCommentSchema, feishuDriveAddCommentHandler, feishuCheck,
		[]string{"FEISHU_APP_ID", "FEISHU_APP_SECRET"}, false,
		"Add a whole-document comment on a Feishu document", "💬")
}

// Silencing unused import warnings
var _ = json.Marshal
