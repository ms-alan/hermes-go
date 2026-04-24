package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/nousresearch/hermes-go/pkg/agent"
	agentcontext "github.com/nousresearch/hermes-go/pkg/context"
	"github.com/nousresearch/hermes-go/pkg/config"
	"github.com/nousresearch/hermes-go/pkg/cron"
	"github.com/nousresearch/hermes-go/pkg/gateway"
	"github.com/nousresearch/hermes-go/pkg/gateway/qqbot"
	"github.com/nousresearch/hermes-go/pkg/model"
	"github.com/nousresearch/hermes-go/pkg/session"
	"github.com/nousresearch/hermes-go/pkg/skill"
	"github.com/nousresearch/hermes-go/pkg/tools"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	platformsFlag := flag.String("platforms", "qq", "comma-separated platforms (qq,telegram,discord)")
	sessionIDFlag := flag.String("session", "", "session ID to resume")
	skillsDir := flag.String("skills-dir", "", "skills directory (default ~/.hermes/skills)")
	gatewayAddr := flag.String("gateway", "", "HTTP API address (host:port)")
	flag.Parse()

	logger := slog.Default()

	// Skills directory
	skDir := *skillsDir
	if skDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			skDir = fmt.Sprintf("%s/.hermes/skills", home)
		}
	}
	var skillLoader *skill.Loader
	if skDir != "" {
		skillLoader = skill.NewLoader(skDir, logger)
		if err := skillLoader.LoadAll(); err != nil {
			logger.Warn("skill load warnings", "error", err)
		}
		skill.SetLoader(skillLoader)
	}

	// Load configuration from ~/.hermes/config.yaml.
	home, err := os.UserHomeDir()
	if err != nil {
		logger.Warn("cannot determine home directory, using /tmp")
		home = "/tmp"
	}
	cfgPath := filepath.Join(home, ".hermes", "config.yaml")
	loader := config.NewLoader(config.WithConfigFiles(cfgPath), config.WithLogger(logger))
	cfg, err := loader.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config load error:", err)
		os.Exit(1)
	}

	// Session store
	store, err := session.NewStore()
	if err != nil {
		fmt.Fprintln(os.Stderr, "session store error:", err)
		os.Exit(1)
	}
	defer store.Close()

	// Model client — build from config (env vars override)
	baseURL := cfg.Model.APIBase
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	apiKey := cfg.Model.APIKey
	if apiKey == "" {
		apiKey = os.Getenv("OPENAI_API_KEY")
	}
	modelName := cfg.Model.ModelName
	if modelName == "" {
		modelName = "gpt-4o"
	}
	modelClient, err := model.NewOpenAIClient(
		model.WithAPIKey(apiKey),
		model.WithBaseURL(baseURL),
		model.WithModel(modelName),
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, "model client error:", err)
		os.Exit(1)
	}

	// Context manager
	ctxMgr := agentcontext.NewManager(
		agentcontext.DefaultManagerConfig(200000),
		logger,
		modelClient,
	)

	// AIAgent
	agentCfg := agent.Config{
		Model:         envOr("HERMES_MODEL", "gpt-4o"),
		MaxIterations: 90,
		Logger:        logger,
	}
	aiAgent := agent.NewAIAgent(modelClient, agentCfg)

	// Register built-in tools so gateway can handle tool calls from QQ messages
	tools.RegisterBuiltinToolsToAgent(aiAgent)
	aiAgent.SyncToolsToConfig()

	// Session agent
	sessAgent := agent.NewSessionAgent(aiAgent, store, ctxMgr, logger)

	// Resume existing session if -session flag provided
	if *sessionIDFlag != "" {
		if err := sessAgent.Switch(*sessionIDFlag); err != nil {
			logger.Warn("failed to resume session, starting new session", "session_id", *sessionIDFlag, "error", err)
			if _, err := sessAgent.New("gateway", modelName, ""); err != nil {
				logger.Error("failed to create session", "error", err)
			}
		} else {
			logger.Info("resumed session", "session_id", *sessionIDFlag)
		}
	}

	// Platform adapters
	var adapters []gateway.PlatformAdapter
	var qqAdapter *qqbot.Adapter
	var httpServer *httpServer // nil unless -gateway flag is set

	if containsStr(*platformsFlag, "qq") {
		qqCfg := qqbot.DefaultConfig()
		if qqCfg != nil {
			var err error
			qqAdapter, err = qqbot.NewAdapter(qqCfg, logger)
			if err != nil {
				logger.Warn("QQ adapter error", "error", err)
			} else {
				h := &qqHandler{agent: sessAgent, adapter: qqAdapter, logger: logger}
				qqAdapter.Handler = h
				if err := qqAdapter.Connect(ctx); err != nil {
					logger.Error("QQ connect failed", "error", err)
				} else {
					adapters = append(adapters, qqAdapter)
					gateway.RegisterAdapter(qqAdapter)
					logger.Info("QQ adapter connected")
				}
			}
		} else {
			logger.Warn("QQ not configured (set QQ_APP_ID and QQ_CLIENT_SECRET)")
		}
	}

	// Telegram adapter
	if containsStr(*platformsFlag, "telegram") {
		telegramCfg := gateway.TelegramConfig{
			BotToken: os.Getenv("TELEGRAM_BOT_TOKEN"),
		}
		telegramAdapter := gateway.NewTelegramAdapter(telegramCfg, logger)
		if err := telegramAdapter.Connect(ctx); err != nil {
			logger.Warn("Telegram adapter error", "error", err)
		} else {
			adapters = append(adapters, telegramAdapter)
			logger.Info("Telegram adapter connected")
			if httpServer != nil {
				httpServer.RegisterTelegramAdapter(telegramAdapter)
				logger.Info("Telegram webhook endpoint: POST /telegram/webhook")
			}
		}
	}

	// Discord adapter
	if containsStr(*platformsFlag, "discord") {
		discordCfg := gateway.DiscordConfig{
			BotToken:      os.Getenv("DISCORD_BOT_TOKEN"),
			ApplicationID: os.Getenv("DISCORD_APPLICATION_ID"),
			WebhookURL:    os.Getenv("DISCORD_WEBHOOK_URL"),
		}
		discordAdapter := gateway.NewDiscordAdapter(discordCfg, logger)
		if err := discordAdapter.Connect(ctx); err != nil {
			logger.Warn("Discord adapter error", "error", err)
		} else {
			adapters = append(adapters, discordAdapter)
			logger.Info("Discord adapter connected")
			if httpServer != nil {
				httpServer.RegisterDiscordAdapter(discordAdapter)
				logger.Info("Discord webhook endpoint: POST /discord/webhook")
			}
		}
	}

	// Cron scheduler — wire job runner + QQ deliverer
	var cronScheduler *cron.Scheduler
	{
		cronStore, err := cron.NewStore("")
		if err != nil {
			logger.Warn("cron store error", "error", err)
		} else {
			tools.SetCronStore(cronStore)

			// Create AI runner
			var cronRunner *cron.AicallRunner
			if sessAgent != nil {
				cronRunner = &cron.AicallRunner{
					SessionAgent: sessAgent,
					SkillLoader:  skillLoader,
					Logger:       logger,
				}
			}

			// Create QQ deliverer if adapter is connected
			var cronDeliverer cron.Deliverer
			if qqAdapter != nil {
				cronDeliverer = cron.NewQQDeliverer(qqAdapter)
			}

			if cronRunner != nil {
				cronScheduler = cron.NewScheduler(cronStore, cronRunner, cronDeliverer, logger)
				cronScheduler.Start()
				logger.Info("cron scheduler started")
			}
		}
	}

	// HTTP API server (optional)
	if *gatewayAddr != "" {
		srv := newHTTPServer(sessAgent, logger)
		httpServer = srv
		srv.Addr = *gatewayAddr
		go func() {
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logger.Error("HTTP server error", "error", err)
			}
		}()
	}

	// Wait for shutdown
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	logger.Info("shutting down...")
	if httpServer != nil {
		if err := httpServer.Shutdown(ctx); err != nil {
			logger.Error("HTTP server shutdown", "error", err)
		}
	}
	if cronScheduler != nil {
		cronScheduler.Stop()
	}
	for _, a := range adapters {
		a.Disconnect(ctx)
	}
}

// qqHandler dispatches QQ messages to the session agent.
type qqHandler struct {
	agent   *agent.SessionAgent
	adapter gateway.PlatformAdapter
	logger  *slog.Logger
}

func (h *qqHandler) HandleInbound(ctx context.Context, msg *gateway.InboundMessage) error {
	// Build delivery origin so tools (e.g. cron) know where to deliver.
	ctx = tools.WithDeliveryOrigin(ctx, &cron.DeliveryOrigin{
		Platform:  string(msg.Platform),
		ChatID:    msg.ChatID,
		UserID:    msg.UserID,
		SessionID: h.agent.SessionID(),
	})

	response, err := h.agent.Chat(ctx, msg.Content)
	if err != nil {
		h.logger.Error("chat error", "error", err)
		response = "抱歉，处理消息时出错了。"
	}
	// Use Send instead of SendText so markdown and image_path are supported.
	result, err := h.adapter.Send(ctx, &gateway.OutboundMessage{
		Platform: gateway.PlatformQQ,
		ChatID:   msg.ChatID,
		UserID:   msg.UserID,
		Content:  response,
	})
	if err != nil {
		h.logger.Error("send error", "chat_id", msg.ChatID, "error", err)
		return fmt.Errorf("send failed: %w", err)
	}
	if !result.Success {
		h.logger.Warn("send reported failure", "chat_id", msg.ChatID, "error", result.Error)
		return fmt.Errorf("send unsuccessful: %s", result.Error)
	}
	return nil
}

func envOr(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func containsStr(list, target string) bool {
	for _, p := range strings.Split(list, ",") {
		if strings.TrimSpace(p) == target {
			return true
		}
	}
	return false
}
