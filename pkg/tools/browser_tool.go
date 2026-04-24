package tools

import (
	"context"
	"fmt"

	"github.com/nousresearch/hermes-go/pkg/tools/browser"
)

var browserManager = browser.NewManager(nil)

// ---------------------------------------------------------------------------
// Tab management tools
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Tab handlers
// ---------------------------------------------------------------------------

func browserNewTabHandler(args map[string]any) string {
	url, _ := args["url"].(string)
	ctx := context.Background()
	tabID, err := browserManager.NewTab(ctx)
	if err != nil {
		return toolError(fmt.Sprintf("new tab failed: %v", err))
	}
	if url != "" {
		if _, err := browserManager.Navigate(ctx, url); err != nil {
			return toolError(fmt.Sprintf("opened tab %s but navigate to %s failed: %v", tabID, url, err))
		}
	}
	return toolResultData(map[string]any{
		"tab_id":  tabID,
		"url":     url,
		"message": fmt.Sprintf("New tab %s created and active", tabID),
	})
}

func browserSwitchTabHandler(args map[string]any) string {
	tabID, _ := args["tab_id"].(string)
	if tabID == "" {
		return toolError("tab_id is required")
	}
	if err := browserManager.SwitchTab(tabID); err != nil {
		return toolError(err.Error())
	}
	return toolResultData(map[string]any{
		"tab_id":  tabID,
		"message": fmt.Sprintf("Switched to tab %s", tabID),
	})
}

func browserCloseTabHandler(args map[string]any) string {
	tabID, _ := args["tab_id"].(string)
	if tabID == "" {
		return toolError("tab_id is required")
	}
	if err := browserManager.CloseTab(tabID); err != nil {
		return toolError(err.Error())
	}
	return toolResultData(map[string]any{
		"tab_id":  tabID,
		"message": fmt.Sprintf("Closed tab %s", tabID),
	})
}

func browserListTabsHandler(args map[string]any) string {
	tabs := browserManager.ListTabs()
	active := browserManager.ActiveTab()
	entries := make([]map[string]any, len(tabs))
	for i, t := range tabs {
		entries[i] = map[string]any{
			"tab_id": t.ID,
			"title":  t.Title,
			"url":    t.URL,
			"active": t.ID == active,
		}
	}
	return toolResultData(map[string]any{
		"tabs":      entries,
		"count":     len(tabs),
		"active_id": active,
	})
}

// ---------------------------------------------------------------------------
// Navigation & content tools
// ---------------------------------------------------------------------------

func browserNavigateHandler(args map[string]any) string {
	url, _ := args["url"].(string)
	if url == "" {
		return toolError("url is required")
	}
	ctx := context.Background()
	result, err := browserManager.Navigate(ctx, url)
	if err != nil {
		return toolError(fmt.Sprintf("navigate failed: %v", err))
	}
	return toolResultData(map[string]any{
		"url":    url,
		"result": result,
	})
}

func browserSnapshotHandler(args map[string]any) string {
	ctx := context.Background()
	result, err := browserManager.Snapshot(ctx)
	if err != nil {
		return toolError(fmt.Sprintf("snapshot failed: %v", err))
	}
	return toolResult("snapshot", result)
}

// ---------------------------------------------------------------------------
// Interaction tools
// ---------------------------------------------------------------------------

func browserClickHandler(args map[string]any) string {
	ref, _ := args["ref"].(string)
	if ref == "" {
		return toolError("ref is required")
	}
	ctx := context.Background()
	if err := browserManager.Click(ctx, ref); err != nil {
		return toolError(fmt.Sprintf("click failed: %v", err))
	}
	return toolResult("clicked", ref)
}

func browserTypeHandler(args map[string]any) string {
	ref, _ := args["ref"].(string)
	text, _ := args["text"].(string)
	if ref == "" {
		return toolError("ref is required")
	}
	if text == "" {
		return toolError("text is required")
	}
	ctx := context.Background()
	if err := browserManager.Type(ctx, ref, text); err != nil {
		return toolError(fmt.Sprintf("type failed: %v", err))
	}
	return toolResult("typed", fmt.Sprintf("%s → %s", ref, text))
}

func browserBackHandler(args map[string]any) string {
	ctx := context.Background()
	if err := browserManager.Back(ctx); err != nil {
		return toolError(fmt.Sprintf("back failed: %v", err))
	}
	return toolResult("navigated", "back")
}

func browserScrollHandler(args map[string]any) string {
	dir, _ := args["direction"].(string)
	if dir == "" {
		dir = "down"
	}
	if dir != "up" && dir != "down" {
		return toolError("direction must be 'up' or 'down'")
	}
	ctx := context.Background()
	if err := browserManager.Scroll(ctx, dir); err != nil {
		return toolError(fmt.Sprintf("scroll failed: %v", err))
	}
	return toolResult("scrolled", dir)
}

func browserPressHandler(args map[string]any) string {
	key, _ := args["key"].(string)
	if key == "" {
		return toolError("key is required")
	}
	ctx := context.Background()
	if err := browserManager.Press(ctx, key); err != nil {
		return toolError(fmt.Sprintf("press failed: %v", err))
	}
	return toolResult("pressed", key)
}

// CloseBrowser closes the browser.
func CloseBrowser() {
	browserManager.Close()
}

// ---------------------------------------------------------------------------
// browser_get_images
// ---------------------------------------------------------------------------

var browserGetImagesSchema = map[string]any{
	"name":        "browser_get_images",
	"description": "Get a list of all images on the current page with their URLs and alt text. Useful for finding images to analyze with the vision tool. Requires browser_navigate to be called first.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{},
	},
}

func browserGetImagesHandler(args map[string]any) string {
	ctx := context.Background()
	jsonStr, err := browserManager.GetImages(ctx)
	if err != nil {
		return toolError(fmt.Sprintf("get images failed: %v", err))
	}
	return toolResultData(map[string]any{
		"images": jsonStr,
	})
}

// ---------------------------------------------------------------------------
// browser_console
// ---------------------------------------------------------------------------

var browserConsoleSchema = map[string]any{
	"name":        "browser_console",
	"description": "Evaluate JavaScript in the page context — use for DOM inspection, reading page state, or extracting data programmatically. Also accepts no expression to return basic info. Requires browser_navigate to be called first.",
	"parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"expression": map[string]any{
				"type":        "string",
				"description": "JavaScript expression to evaluate. Example: 'document.title' or 'document.querySelectorAll(\"a\").length'",
			},
		},
	},
}

func browserConsoleHandler(args map[string]any) string {
	ctx := context.Background()
	expression, _ := args["expression"].(string)

	// If no expression, return a simple page info summary
	if expression == "" {
		summary, err := browserManager.Snapshot(ctx)
		if err != nil {
			return toolError(fmt.Sprintf("snapshot failed: %v", err))
		}
		return toolResultData(map[string]any{
			"message": "No expression provided. Page snapshot above.",
			"snapshot": summary,
		})
	}

	result, err := browserManager.EvaluateJS(ctx, expression)
	if err != nil {
		return toolError(fmt.Sprintf("evaluate failed: %v", err))
	}
	return toolResultData(map[string]any{
		"expression": expression,
		"result":     result,
	})
}
