// Package browser provides browser automation tools using Chrome DevTools Protocol.
// Supports local Chromium/Chrome via chromedp with multi-tab management.
package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/google/uuid"
)

// Tab represents a single browser tab.
type Tab struct {
	ID    string // unique tab ID
	Title string
	URL   string
}

// Manager maintains a Chrome browser instance with multiple tabs.
type Manager struct {
	logger    *slog.Logger
	mu        sync.RWMutex
	instance  *chromeInstance
	tabs      map[string]*tabEntry
	activeTab string // active tab ID
}

type tabEntry struct {
	browserCtx context.Context // the chrome browser context
	cancel     context.CancelFunc
	tabCtx     context.Context // per-tab CDP context
	tab        Tab
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
		globalManager = &Manager{
			logger: logger,
			tabs:   make(map[string]*tabEntry),
		}
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

// Close shuts down the browser and all tabs.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.instance != nil {
		m.instance.cancel()
		m.instance = nil
		for _, t := range m.tabs {
			t.cancel()
		}
		m.tabs = make(map[string]*tabEntry)
		m.activeTab = ""
		m.logger.Info("browser: Chrome stopped")
	}
}

// ---------------------------------------------------------------------------
// Tab management
// ---------------------------------------------------------------------------

// NewTab creates a new browser tab and returns its ID.
func (m *Manager) NewTab(ctx context.Context) (string, error) {
	if err := m.EnsureChrome(ctx); err != nil {
		return "", err
	}

	tabCtx, cancel := chromedp.NewContext(ctx)
	if tabCtx == nil {
		return "", fmt.Errorf("failed to create tab context")
	}

	id := uuid.NewString()[:8]

	m.mu.Lock()
	defer m.mu.Unlock()

	m.tabs[id] = &tabEntry{
		browserCtx: ctx,
		cancel:     cancel,
		tabCtx:     tabCtx,
		tab: Tab{ID: id},
	}
	m.activeTab = id

	return id, nil
}

// SwitchTab activates an existing tab by ID.
func (m *Manager) SwitchTab(tabID string) error {
	m.mu.RLock()
	_, ok := m.tabs[tabID]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("no such tab: %s", tabID)
	}
	m.mu.Lock()
	m.activeTab = tabID
	m.mu.Unlock()
	return nil
}

// CloseTab closes a specific tab by ID.
func (m *Manager) CloseTab(tabID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.tabs[tabID]
	if !ok {
		return fmt.Errorf("no such tab: %s", tabID)
	}
	entry.cancel()
	delete(m.tabs, tabID)
	if m.activeTab == tabID {
		// Switch to first remaining tab
		for id := range m.tabs {
			m.activeTab = id
			return nil
		}
		m.activeTab = ""
	}
	return nil
}

// ListTabs returns all tabs.
func (m *Manager) ListTabs() []Tab {
	m.mu.RLock()
	defer m.mu.RUnlock()
	tabs := make([]Tab, 0, len(m.tabs))
	for _, t := range m.tabs {
		tabs = append(tabs, t.tab)
	}
	return tabs
}

// ActiveTab returns the active tab ID.
func (m *Manager) ActiveTab() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.activeTab
}

// getTabCtx returns the chromedp context for the active tab.
func (m *Manager) getTabCtx() (context.Context, func()) {
	m.mu.RLock()
	tabID := m.activeTab
	m.mu.RUnlock()
	if tabID == "" {
		return nil, func() {}
	}
	m.mu.RLock()
	entry, ok := m.tabs[tabID]
	m.mu.RUnlock()
	if !ok {
		return nil, func() {}
	}
	return entry.tabCtx, func() {}
}

// ---------------------------------------------------------------------------
// Navigation & content
// ---------------------------------------------------------------------------

// Navigate opens a URL in a new tab or the active tab.
func (m *Manager) Navigate(ctx context.Context, url string) (string, error) {
	tabCtx, cancel := m.getTabCtx()
	if tabCtx == nil {
		// No active tab — create one
		newID, err := m.NewTab(ctx)
		if err != nil {
			return "", err
		}
		m.mu.RLock()
		entry := m.tabs[newID]
		m.mu.RUnlock()
		tabCtx = entry.tabCtx
	}
	defer cancel()

	var result navResult
	if err := chromedp.Run(tabCtx,
		chromedp.Navigate(url),
		chromedp.Sleep(1*time.Second),
		chromedp.Location(&result.URL),
		chromedp.Title(&result.Title),
		chromedp.Evaluate(pageWalkScript(), &result.TextContent),
	); err != nil {
		return "", fmt.Errorf("navigate to %s: %w", url, err)
	}

	// Update tab metadata
	m.mu.Lock()
	if entry, ok := m.tabs[m.activeTab]; ok {
		entry.tab.URL = result.URL
		entry.tab.Title = result.Title
	}
	m.mu.Unlock()

	return fmt.Sprintf("Tab: %s | URL: %s\nTitle: %s\n\nContent:\n%s", m.activeTab, result.URL, result.Title, result.TextContent), nil
}

// Snapshot returns a text snapshot of the current tab.
func (m *Manager) Snapshot(ctx context.Context) (string, error) {
	tabCtx, cancel := m.getTabCtx()
	if tabCtx == nil {
		return "", fmt.Errorf("no active tab — use browser_navigate first")
	}
	defer cancel()

	var result navResult
	if err := chromedp.Run(tabCtx,
		chromedp.Sleep(500*time.Millisecond),
		chromedp.Location(&result.URL),
		chromedp.Title(&result.Title),
		chromedp.Evaluate(pageWalkScript(), &result.TextContent),
	); err != nil {
		return "", fmt.Errorf("snapshot: %w", err)
	}

	return fmt.Sprintf("Tab: %s | URL: %s\nTitle: %s\n\nContent:\n%s", m.activeTab, result.URL, result.Title, result.TextContent), nil
}

// Screenshot takes a screenshot and returns the file path.
func (m *Manager) Screenshot(ctx context.Context) (string, error) {
	tabCtx, cancel := m.getTabCtx()
	if tabCtx == nil {
		return "", fmt.Errorf("no active tab")
	}
	defer cancel()

	var buf []byte
	if err := chromedp.Run(tabCtx,
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

// GetImages returns all images on the current page (src, alt, width, height).
func (m *Manager) GetImages(ctx context.Context) (string, error) {
	tabCtx, cancel := m.getTabCtx()
	if tabCtx == nil {
		return "", fmt.Errorf("no active tab")
	}
	defer cancel()

	var jsonStr string
	jsCode := `JSON.stringify([...document.images].map(img => ({
		src: img.src, alt: img.alt || '',
		width: img.naturalWidth, height: img.naturalHeight
	})).filter(img => img.src && !img.src.startsWith('data:')))`
	if err := chromedp.Run(tabCtx,
		chromedp.Sleep(300*time.Millisecond),
		chromedp.Evaluate(jsCode, &jsonStr),
	); err != nil {
		return "", fmt.Errorf("get images: %w", err)
	}

	var images []map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &images); err != nil {
		return jsonStr, nil // return raw JSON on parse failure
	}
	return jsonStr, nil
}

// EvaluateJS runs arbitrary JavaScript in the page context and returns the result.
func (m *Manager) EvaluateJS(ctx context.Context, expression string) (string, error) {
	tabCtx, cancel := m.getTabCtx()
	if tabCtx == nil {
		return "", fmt.Errorf("no active tab")
	}
	defer cancel()

	var result any
	if err := chromedp.Run(tabCtx,
		chromedp.Sleep(200*time.Millisecond),
		chromedp.Evaluate(expression, &result),
	); err != nil {
		return "", fmt.Errorf("evaluate js: %w", err)
	}
	out, err := json.Marshal(result)
	if err != nil {
		return fmt.Sprintf("%v", result), nil
	}
	return string(out), nil
}

// ---------------------------------------------------------------------------
// Interaction
// ---------------------------------------------------------------------------

// Click clicks an element by ref (@e5) or CSS selector.
func (m *Manager) Click(ctx context.Context, ref string) error {
	return m.clickOrType(ctx, ref, "", false)
}

// Type types text into an element.
func (m *Manager) Type(ctx context.Context, ref string, text string) error {
	return m.clickOrType(ctx, ref, text, true)
}

func (m *Manager) clickOrType(ctx context.Context, ref string, text string, doType bool) error {
	tabCtx, cancel := m.getTabCtx()
	if tabCtx == nil {
		return fmt.Errorf("no active tab")
	}
	defer cancel()

	selector := refToSelector(ref)
	var actions []chromedp.Action
	actions = append(actions, chromedp.Sleep(300*time.Millisecond))
	if doType {
		actions = append(actions, chromedp.SetValue(selector, text, chromedp.NodeVisible))
	} else {
		actions = append(actions, chromedp.Click(selector, chromedp.NodeVisible))
	}
	return chromedp.Run(tabCtx, actions...)
}

// Back navigates back in the current tab.
func (m *Manager) Back(ctx context.Context) error {
	tabCtx, cancel := m.getTabCtx()
	if tabCtx == nil {
		return fmt.Errorf("no active tab")
	}
	defer cancel()
	return chromedp.Run(tabCtx,
		chromedp.NavigateBack(),
		chromedp.Sleep(500*time.Millisecond),
	)
}

// Scroll scrolls up or down in the current tab.
func (m *Manager) Scroll(ctx context.Context, dir string) error {
	tabCtx, cancel := m.getTabCtx()
	if tabCtx == nil {
		return fmt.Errorf("no active tab")
	}
	defer cancel()

	dy := "window.innerHeight"
	if dir == "up" {
		dy = "-window.innerHeight"
	}
	return chromedp.Run(tabCtx,
		chromedp.Evaluate(fmt.Sprintf("window.scrollBy(0,%s)", dy), nil),
	)
}

// Press presses a keyboard key in the current tab.
func (m *Manager) Press(ctx context.Context, key string) error {
	tabCtx, cancel := m.getTabCtx()
	if tabCtx == nil {
		return fmt.Errorf("no active tab")
	}
	defer cancel()
	return chromedp.Run(tabCtx, chromedp.KeyEvent(key))
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

type chromeInstance struct {
	cancel context.CancelFunc
}

type navResult struct {
	URL         string
	Title       string
	TextContent string
}

// pageWalkScript returns the JS that extracts text content from the page.
func pageWalkScript() string {
	return `(function(){
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
	})()`
}

// refToSelector converts @e5 → XPath, otherwise uses as CSS selector.
func refToSelector(ref string) string {
	if len(ref) >= 2 && ref[0] == '@' {
		return fmt.Sprintf(`//*[@data-ref="%s"]`, ref[1:])
	}
	if len(ref) > 0 {
		c := ref[0]
		if c == '#' || c == '.' || c == '[' || c == '(' {
			return ref
		}
	}
	return fmt.Sprintf(`//*[contains(text(),"%s")]`, ref)
}
