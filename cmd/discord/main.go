package main

import (
	"bytes"
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
	GuildID      string
	PollInterval time.Duration
}

type PendingDevice struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type PendingDevicesResponse struct {
	PendingDevices []PendingDevice `json:"pending_devices"`
}

type TagsResponse struct {
	Tags []string `json:"tags"`
}

type ApproveRequest struct {
	Tags []string `json:"tags"`
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

	guildID := os.Getenv("DISCORD_GUILD_ID") // optional: empty = global command

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
		GuildID:      guildID,
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

	registeredCmd, err := dg.ApplicationCommandCreate(dg.State.User.ID, cfg.GuildID, cmd)
	if err != nil {
		slog.Error("Failed to register slash command", "error", err)
		os.Exit(1)
	}
	slog.Info("Registered slash command", "name", registeredCmd.Name, "guildID", cfg.GuildID)

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

	// Handle button and select menu interactions
	dg.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		if i.Type != discordgo.InteractionMessageComponent {
			return
		}

		customID := i.MessageComponentData().CustomID
		if strings.HasPrefix(customID, "select_tags:") {
			handleSelectMenu(s, i, cfg, httpClient)
		} else {
			handleButtonClick(s, i, cfg, httpClient)
		}
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

func fetchAvailableTags(cfg Config, httpClient *http.Client) ([]string, error) {
	resp, err := httpClient.Get(cfg.APIURL + "/tags")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("controller returned status %d", resp.StatusCode)
	}

	var res TagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}

	return res.Tags, nil
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

	switch action {
	case "approve":
		// Fetch available tags and show select menu
		tags, err := fetchAvailableTags(cfg, httpClient)
		if err != nil {
			slog.Error("Failed to fetch tags", "error", err)
			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "Failed to fetch available tags: " + err.Error(),
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		// Build select menu options
		options := make([]discordgo.SelectMenuOption, len(tags))
		for idx, tag := range tags {
			options[idx] = discordgo.SelectMenuOption{
				Label: tag,
				Value: tag,
			}
		}

		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseUpdateMessage,
			Data: &discordgo.InteractionResponseData{
				Content: fmt.Sprintf("**Select tags to apply**\nDevice ID: `%s`", deviceID),
				Components: []discordgo.MessageComponent{
					discordgo.ActionsRow{
						Components: []discordgo.MessageComponent{
							discordgo.SelectMenu{
								CustomID:    "select_tags:" + deviceID,
								Placeholder: "Select tags to apply...",
								MinValues:   intPtr(1),
								MaxValues:   len(options),
								Options:     options,
							},
						},
					},
					discordgo.ActionsRow{
						Components: []discordgo.MessageComponent{
							discordgo.Button{
								Label:    "Cancel",
								Style:    discordgo.SecondaryButton,
								CustomID: "cancel:" + deviceID,
							},
						},
					},
				},
			},
		})

	case "decline":
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseDeferredMessageUpdate,
		})

		resp, err := httpClient.Post(cfg.APIURL+"/decline/"+deviceID, "application/json", nil)
		if err != nil {
			slog.Error("Failed to call controller", "error", err)
			s.ChannelMessageSend(i.ChannelID, fmt.Sprintf("Failed to decline device: %s", err.Error()))
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			slog.Error("Controller returned error", "status", resp.StatusCode)
			s.ChannelMessageSend(i.ChannelID, fmt.Sprintf("Failed to decline device: %s", resp.Status))
			return
		}

		s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content:    ptr(fmt.Sprintf("❌ **Declined** by %s", i.Member.User.Username)),
			Components: &[]discordgo.MessageComponent{},
		})

	case "cancel":
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseUpdateMessage,
			Data: &discordgo.InteractionResponseData{
				Content:    "🚫 **Cancelled**",
				Components: []discordgo.MessageComponent{},
			},
		})
	}
}

func handleSelectMenu(s *discordgo.Session, i *discordgo.InteractionCreate, cfg Config, httpClient *http.Client) {
	customID := i.MessageComponentData().CustomID
	parts := strings.SplitN(customID, ":", 2)
	if len(parts) != 2 || parts[0] != "select_tags" {
		return
	}

	deviceID := parts[1]
	selectedTags := i.MessageComponentData().Values

	slog.Info("Tags selected", "deviceID", deviceID, "tags", selectedTags, "user", i.Member.User.Username)

	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredMessageUpdate,
	})

	// Call approve API with selected tags
	reqBody, _ := json.Marshal(ApproveRequest{Tags: selectedTags})
	resp, err := httpClient.Post(cfg.APIURL+"/approve/"+deviceID, "application/json", bytes.NewReader(reqBody))
	if err != nil {
		slog.Error("Failed to call controller", "error", err)
		s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content:    ptr(fmt.Sprintf("Failed to approve device: %s", err.Error())),
			Components: &[]discordgo.MessageComponent{},
		})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.Error("Controller returned error", "status", resp.StatusCode)
		s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content:    ptr(fmt.Sprintf("Failed to approve device: %s", resp.Status)),
			Components: &[]discordgo.MessageComponent{},
		})
		return
	}

	s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content:    ptr(fmt.Sprintf("✅ **Approved** by %s\nTags: `%s`", i.Member.User.Username, strings.Join(selectedTags, "`, `"))),
		Components: &[]discordgo.MessageComponent{},
	})
}

func ptr(s string) *string {
	return &s
}

func intPtr(i int) *int {
	return &i
}
