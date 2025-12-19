package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
)

type Config struct {
	BotToken     string
	APIURL       string
	ChannelID    string
	PollInterval time.Duration
}

type PendingDevice struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type PendingDevicesResponse struct {
	PendingDevices []PendingDevice `json:"pending_devices"`
}

func loadConfig() (Config, error) {
	botToken := os.Getenv("DISCORD_BOT_TOKEN")
	if botToken == "" {
		return Config{}, errors.New("DISCORD_BOT_TOKEN is required")
	}

	apiURL := os.Getenv("API_URL")
	if apiURL == "" {
		apiURL = "http://localhost:8080"
	}

	channelID := os.Getenv("DISCORD_CHANNEL_ID")
	if channelID == "" {
		return Config{}, errors.New("DISCORD_CHANNEL_ID is required")
	}

	pollInterval := 24 * time.Hour
	if pollIntervalStr := os.Getenv("POLL_INTERVAL"); pollIntervalStr != "" {
		parsed, err := time.ParseDuration(pollIntervalStr)
		if err != nil {
			return Config{}, errors.New("POLL_INTERVAL must be a valid duration (e.g., 24h, 1h30m)")
		}
		pollInterval = parsed
	}

	return Config{
		BotToken:     botToken,
		APIURL:       apiURL,
		ChannelID:    channelID,
		PollInterval: pollInterval,
	}, nil
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := loadConfig()
	if err != nil {
		slog.Error("Failed to load config", "error", err)
		os.Exit(1)
	}

	dg, err := discordgo.New("Bot " + cfg.BotToken)
	if err != nil {
		slog.Error("Failed to create Discord session", "error", err)
		os.Exit(1)
	}

	if err := dg.Open(); err != nil {
		slog.Error("Failed to open Discord connection", "error", err)
		os.Exit(1)
	}
	defer dg.Close()

	// Register slash command
	cmd := &discordgo.ApplicationCommand{
		Name:        "tailscale-approve",
		Description: "Check and approve pending Tailscale devices",
	}

	registeredCmd, err := dg.ApplicationCommandCreate(dg.State.User.ID, "", cmd)
	if err != nil {
		slog.Error("Failed to register slash command", "error", err)
		os.Exit(1)
	}
	slog.Info("Registered slash command", "name", registeredCmd.Name)

	httpClient := &http.Client{Timeout: 30 * time.Second}

	// Handle slash command
	dg.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		if i.Type != discordgo.InteractionApplicationCommand {
			return
		}

		if i.ApplicationCommandData().Name != "tailscale-approve" {
			return
		}

		handleSlashCommand(s, i, cfg, httpClient)
	})

	// Handle button interactions
	dg.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		if i.Type != discordgo.InteractionMessageComponent {
			return
		}

		handleButtonClick(s, i, cfg, httpClient)
	})

	slog.Info("Discord bot started", "apiURL", cfg.APIURL, "pollInterval", cfg.PollInterval)

	// Start automatic polling loop
	go func() {
		ticker := time.NewTicker(cfg.PollInterval)
		defer ticker.Stop()

		for range ticker.C {
			runScheduledCheck(dg, cfg, httpClient)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	slog.Info("Shutting down")
}

func runScheduledCheck(s *discordgo.Session, cfg Config, httpClient *http.Client) {
	slog.Info("Running scheduled check")

	pending, err := fetchPendingDevices(cfg, httpClient)
	if err != nil {
		slog.Error("Scheduled check failed", "error", err)
		return
	}

	if len(pending) == 0 {
		slog.Info("No pending devices found")
		return
	}

	if len(pending) >= 3 {
		s.ChannelMessageSend(cfg.ChannelID, fmt.Sprintf("Warning: %d pending devices found. This is unusual. Please check the Tailscale admin console.", len(pending)))
		return
	}

	for _, device := range pending {
		sendDeviceApprovalMessage(s, cfg.ChannelID, device)
	}
}

func fetchPendingDevices(cfg Config, httpClient *http.Client) ([]PendingDevice, error) {
	resp, err := httpClient.Get(cfg.APIURL + "/pending-devices")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("controller returned status %d", resp.StatusCode)
	}

	var res PendingDevicesResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}

	return res.PendingDevices, nil
}

func handleSlashCommand(s *discordgo.Session, i *discordgo.InteractionCreate, cfg Config, httpClient *http.Client) {
	slog.Info("Slash command invoked", "user", i.Member.User.Username)

	// Acknowledge immediately
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})

	pending, err := fetchPendingDevices(cfg, httpClient)
	if err != nil {
		slog.Error("Failed to get pending devices", "error", err)
		s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: ptr("Failed to get pending devices: " + err.Error()),
		})
		return
	}

	if len(pending) == 0 {
		s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: ptr("No pending devices found."),
		})
		return
	}

	// Too many devices warning
	if len(pending) >= 3 {
		s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: ptr(fmt.Sprintf("Warning: %d pending devices found. This is unusual. Please check the Tailscale admin console.", len(pending))),
		})
		return
	}

	// Send response
	s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: ptr(fmt.Sprintf("Found %d pending device(s). Sending approval requests...", len(pending))),
	})

	// Send individual messages with buttons
	for _, device := range pending {
		sendDeviceApprovalMessage(s, cfg.ChannelID, device)
	}
}

func sendDeviceApprovalMessage(s *discordgo.Session, channelID string, device PendingDevice) {
	_, err := s.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
		Content: fmt.Sprintf("**New device pending approval**\nName: `%s`\nID: `%s`", device.Name, device.ID),
		Components: []discordgo.MessageComponent{
			discordgo.ActionsRow{
				Components: []discordgo.MessageComponent{
					discordgo.Button{
						Label:    "Approve",
						Style:    discordgo.SuccessButton,
						CustomID: "approve:" + device.ID,
					},
					discordgo.Button{
						Label:    "Decline",
						Style:    discordgo.DangerButton,
						CustomID: "decline:" + device.ID,
					},
				},
			},
		},
	})
	if err != nil {
		slog.Error("Failed to send approval message", "device", device.Name, "error", err)
	}
}

func handleButtonClick(s *discordgo.Session, i *discordgo.InteractionCreate, cfg Config, httpClient *http.Client) {
	customID := i.MessageComponentData().CustomID
	parts := strings.SplitN(customID, ":", 2)
	if len(parts) != 2 {
		return
	}

	action := parts[0]
	deviceID := parts[1]

	slog.Info("Button clicked", "action", action, "deviceID", deviceID, "user", i.Member.User.Username)

	// Acknowledge immediately
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredMessageUpdate,
	})

	var endpoint string
	switch action {
	case "approve":
		endpoint = cfg.APIURL + "/approve/" + deviceID
	case "decline":
		endpoint = cfg.APIURL + "/decline/" + deviceID
	default:
		return
	}

	resp, err := httpClient.Post(endpoint, "application/json", nil)
	if err != nil {
		slog.Error("Failed to call controller", "error", err)
		s.ChannelMessageSend(i.ChannelID, fmt.Sprintf("Failed to %s device: %s", action, err.Error()))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.Error("Controller returned error", "status", resp.StatusCode)
		s.ChannelMessageSend(i.ChannelID, fmt.Sprintf("Failed to %s device: %s", action, resp.Status))
		return
	}

	// Update original message
	var resultEmoji string
	var resultText string
	if action == "approve" {
		resultEmoji = "✅"
		resultText = "Approved"
	} else {
		resultEmoji = "❌"
		resultText = "Declined"
	}

	s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content:    ptr(fmt.Sprintf("%s **%s** by %s", resultEmoji, resultText, i.Member.User.Username)),
		Components: &[]discordgo.MessageComponent{},
	})
}

func ptr(s string) *string {
	return &s
}
