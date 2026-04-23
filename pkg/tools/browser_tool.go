package tools

import (
	"context"
	"fmt"

	"github.com/nousresearch/hermes-go/pkg/tools/browser"
)

var browserManager = browser.NewManager(nil)

// browserNavigateHandler navigates to a URL.
func browserNavigateHandler(args map[string]any) string {
	url, _ := args["url"].(string)
	if url == "" {
		return toolError("url is required")
	}
	ctx := context.Background()
	result, err := browser.Navigate(ctx, url)
	if err != nil {
		return toolError(fmt.Sprintf("navigate failed: %v", err))
	}
	return toolResultData(map[string]any{
		"url":    url,
		"result": result,
	})
}

// browserSnapshotHandler returns a text snapshot of the current page.
func browserSnapshotHandler(args map[string]any) string {
	ctx := context.Background()
	result, err := browser.Snapshot(ctx)
	if err != nil {
		return toolError(fmt.Sprintf("snapshot failed: %v", err))
	}
	return toolResult("snapshot", result)
}

// browserVisionHandler takes a screenshot and returns the path.
func browserVisionHandler(args map[string]any) string {
	ctx := context.Background()
	path, err := browser.Screenshot(ctx)
	if err != nil {
		return toolError(fmt.Sprintf("screenshot failed: %v", err))
	}
	question, _ := args["question"].(string)
	return toolResultData(map[string]any{
		"screenshot": path,
		"question":   question,
		"note":       "Screenshot saved. Use vision_analyze to analyze the image.",
	})
}

// browserClickHandler clicks an element.
func browserClickHandler(args map[string]any) string {
	ref, _ := args["ref"].(string)
	if ref == "" {
		return toolError("ref is required")
	}
	ctx := context.Background()
	if err := browser.Click(ctx, ref); err != nil {
		return toolError(fmt.Sprintf("click failed: %v", err))
	}
	return toolResult("clicked", ref)
}

// browserTypeHandler types text into an element.
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
	if err := browser.Type(ctx, ref, text); err != nil {
		return toolError(fmt.Sprintf("type failed: %v", err))
	}
	return toolResult("typed", fmt.Sprintf("%s → %s", ref, text))
}

// browserBackHandler navigates back.
func browserBackHandler(args map[string]any) string {
	ctx := context.Background()
	if err := browser.Back(ctx); err != nil {
		return toolError(fmt.Sprintf("back failed: %v", err))
	}
	return toolResult("navigated", "back")
}

// browserScrollHandler scrolls the page.
func browserScrollHandler(args map[string]any) string {
	dir, _ := args["direction"].(string)
	if dir == "" {
		dir = "down"
	}
	if dir != "up" && dir != "down" {
		return toolError("direction must be 'up' or 'down'")
	}
	ctx := context.Background()
	if err := browser.Scroll(ctx, dir); err != nil {
		return toolError(fmt.Sprintf("scroll failed: %v", err))
	}
	return toolResult("scrolled", dir)
}

// browserPressHandler presses a keyboard key.
func browserPressHandler(args map[string]any) string {
	key, _ := args["key"].(string)
	if key == "" {
		return toolError("key is required")
	}
	ctx := context.Background()
	if err := browser.Press(ctx, key); err != nil {
		return toolError(fmt.Sprintf("press failed: %v", err))
	}
	return toolResult("pressed", key)
}

// browserConsoleHandler returns browser console output.
func browserConsoleHandler(args map[string]any) string {
	return toolResult("console", "Use browser_snapshot to see page content. Console capture requires CDP event subscription.")
}

// browserGetImagesHandler lists images on the current page.
func browserGetImagesHandler(args map[string]any) string {
	return toolResult("images", "Use browser_vision to take a screenshot and analyze images on the page.")
}

// CloseBrowser closes the browser.
func CloseBrowser() {
	browserManager.Close()
}
