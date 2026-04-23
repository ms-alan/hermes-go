// Package browser provides browser automation tools using Chrome DevTools Protocol.
// Supports local Chromium/Chrome via chromedp.
package browser

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/chromedp/chromedp"
)

// Manager maintains a Chrome instance.
type Manager struct {
	logger   *slog.Logger
	mu       sync.RWMutex
	instance *chromeInstance
}

type chromeInstance struct {
	cancel context.CancelFunc
}

var globalManager *Manager
var managerMu sync.RWMutex

// NewManager creates (or returns) the global browser manager.
func NewManager(logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	managerMu.Lock()
	defer managerMu.Unlock()
	if globalManager == nil {
		globalManager = &Manager{logger: logger}
	}
	return globalManager
}

// EnsureChrome starts Chrome if not running.
func (m *Manager) EnsureChrome(ctx context.Context) error {
	m.mu.RLock()
	running := m.instance != nil
	m.mu.RUnlock()
	if running {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.instance != nil {
		return nil
	}

	allocCtx, cancel := chromedp.NewContext(ctx,
		chromedp.WithLogf(func(format string, args ...any) {
			m.logger.Debug(fmt.Sprintf("chromedp: "+format, args...))
		}),
	)

	if err := chromedp.Run(allocCtx); err != nil {
		cancel()
		return fmt.Errorf("start chrome: %w", err)
	}

	m.instance = &chromeInstance{cancel: cancel}
	m.logger.Info("browser: Chrome started")
	return nil
}

// Close shuts down the browser.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.instance != nil {
		m.instance.cancel()
		m.instance = nil
		m.logger.Info("browser: Chrome stopped")
	}
}

// Navigate opens a URL and returns a text summary.
func Navigate(ctx context.Context, url string) (string, error) {
	m := NewManager(nil)
	if err := m.EnsureChrome(ctx); err != nil {
		return "", err
	}

	browserCtx, cancel := chromedp.NewContext(ctx)
	defer cancel()

	var result navResult
	if err := chromedp.Run(browserCtx,
		chromedp.Navigate(url),
		chromedp.Sleep(1*time.Second),
		chromedp.Location(&result.URL),
		chromedp.Title(&result.Title),
		chromedp.Evaluate(`(function(){
			var texts=[];
			function walk(el,depth){
				if(depth>5||!el) return;
				if(el.nodeType===3){
					var t=el.textContent.trim();
					if(t) texts.push(t);
				}else if(el.nodeType===1){
					if(el.children.length===0&&el.textContent.trim()){
						texts.push('['+el.tagName+'] '+el.textContent.trim());
					}
					for(var i=0;i<el.children.length;i++) walk(el.children[i],depth+1);
				}
			}
			walk(document.body,0);
			return texts.join('\n').substring(0,8000);
		})()`, &result.TextContent),
	); err != nil {
		return "", fmt.Errorf("navigate to %s: %w", url, err)
	}

	return fmt.Sprintf("URL: %s\nTitle: %s\n\nContent:\n%s", result.URL, result.Title, result.TextContent), nil
}

// Snapshot returns a text snapshot of the current page.
func Snapshot(ctx context.Context) (string, error) {
	m := globalManager
	if m == nil {
		return "", fmt.Errorf("browser manager not initialized")
	}

	m.mu.RLock()
	running := m.instance != nil
	m.mu.RUnlock()
	if !running {
		return "", fmt.Errorf("browser not running, use browser_navigate first")
	}

	browserCtx, cancel := chromedp.NewContext(ctx)
	defer cancel()

	var result navResult
	if err := chromedp.Run(browserCtx,
		chromedp.Sleep(500*time.Millisecond),
		chromedp.Location(&result.URL),
		chromedp.Title(&result.Title),
		chromedp.Evaluate(`(function(){
			var texts=[];
			function walk(el,depth){
				if(depth>5||!el) return;
				if(el.nodeType===3){
					var t=el.textContent.trim();
					if(t) texts.push(t);
				}else if(el.nodeType===1){
					if(el.children.length===0&&el.textContent.trim()){
						texts.push('['+el.tagName+'] '+el.textContent.trim());
					}
					for(var i=0;i<el.children.length;i++) walk(el.children[i],depth+1);
				}
			}
			walk(document.body,0);
			return texts.join('\n').substring(0,8000);
		})()`, &result.TextContent),
	); err != nil {
		return "", fmt.Errorf("snapshot: %w", err)
	}

	return fmt.Sprintf("URL: %s\nTitle: %s\n\nContent:\n%s", result.URL, result.Title, result.TextContent), nil
}

// Click clicks an element by ref (@e5) or CSS selector.
func Click(ctx context.Context, ref string) error {
	return clickOrType(ctx, ref, "", false)
}

// Type types text into an element.
func Type(ctx context.Context, ref string, text string) error {
	return clickOrType(ctx, ref, text, true)
}

func clickOrType(ctx context.Context, ref string, text string, doType bool) error {
	m := globalManager
	if m == nil {
		return fmt.Errorf("browser not initialized")
	}
	m.mu.RLock()
	running := m.instance != nil
	m.mu.RUnlock()
	if !running {
		return fmt.Errorf("browser not running")
	}

	selector := refToSelector(ref)
	browserCtx, cancel := chromedp.NewContext(ctx)
	defer cancel()

	var actions []chromedp.Action
	actions = append(actions, chromedp.Sleep(300*time.Millisecond))
	if doType {
		actions = append(actions, chromedp.SetValue(selector, text, chromedp.NodeVisible))
	} else {
		actions = append(actions, chromedp.Click(selector, chromedp.NodeVisible))
	}

	return chromedp.Run(browserCtx, actions...)
}

// Back navigates back.
func Back(ctx context.Context) error {
	m := globalManager
	if m == nil || m.instance == nil {
		return fmt.Errorf("browser not running")
	}
	browserCtx, cancel := chromedp.NewContext(ctx)
	defer cancel()
	return chromedp.Run(browserCtx,
		chromedp.NavigateBack(),
		chromedp.Sleep(500*time.Millisecond),
	)
}

// Scroll scrolls up or down.
func Scroll(ctx context.Context, dir string) error {
	m := globalManager
	if m == nil || m.instance == nil {
		return fmt.Errorf("browser not running")
	}
	dy := "window.innerHeight"
	if dir == "up" {
		dy = "-window.innerHeight"
	}
	browserCtx, cancel := chromedp.NewContext(ctx)
	defer cancel()
	return chromedp.Run(browserCtx,
		chromedp.Evaluate(fmt.Sprintf("window.scrollBy(0,%s)", dy), nil),
	)
}

// Screenshot takes a screenshot and returns the file path.
func Screenshot(ctx context.Context) (string, error) {
	m := globalManager
	if m == nil || m.instance == nil {
		return "", fmt.Errorf("browser not running")
	}
	browserCtx, cancel := chromedp.NewContext(ctx)
	defer cancel()

	var buf []byte
	if err := chromedp.Run(browserCtx,
		chromedp.Sleep(300*time.Millisecond),
		chromedp.CaptureScreenshot(&buf),
	); err != nil {
		return "", fmt.Errorf("screenshot: %w", err)
	}

	tmp := fmt.Sprintf("/tmp/hermes-browser-%d.png", time.Now().UnixNano())
	if err := os.WriteFile(tmp, buf, 0600); err != nil {
		return "", fmt.Errorf("save screenshot: %w", err)
	}
	return tmp, nil
}

// Press presses a keyboard key.
func Press(ctx context.Context, key string) error {
	m := globalManager
	if m == nil || m.instance == nil {
		return fmt.Errorf("browser not running")
	}
	browserCtx, cancel := chromedp.NewContext(ctx)
	defer cancel()
	return chromedp.Run(browserCtx, chromedp.KeyEvent(key))
}

type navResult struct {
	URL         string
	Title      string
	TextContent string
}

// refToSelector converts @e5 → XPath, otherwise uses as CSS selector.
func refToSelector(ref string) string {
	if len(ref) >= 2 && ref[0] == '@' {
		return fmt.Sprintf(`//*[@data-ref="%s"]`, ref[1:])
	}
	// Common CSS selector prefixes
	if len(ref) > 0 {
		c := ref[0]
		if c == '#' || c == '.' || c == '[' || c == '(' {
			return ref
		}
	}
	// Text contains selector
	return fmt.Sprintf(`//*[contains(text(),"%s")]`, ref)
}
