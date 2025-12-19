package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	tsclient "github.com/tailscale/tailscale-client-go/v2"
)

type Config struct {
	Tailnet     string
	APIKey      string
	TagsToApply []string
	HTTPPort    string
}

type Device struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Authorized bool     `json:"authorized"`
	Tags       []string `json:"tags"`
}

type PendingDevice struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type PendingDevicesResponse struct {
	PendingDevices []PendingDevice `json:"pending_devices"`
}

type DevicesClient interface {
	List(ctx context.Context) ([]Device, error)
	SetTags(ctx context.Context, deviceID string, tags []string) error
}

type tailscaleDevicesClient struct {
	client *tsclient.Client
}

func (c *tailscaleDevicesClient) List(ctx context.Context) ([]Device, error) {
	devices, err := c.client.Devices().List(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]Device, len(devices))
	for i, d := range devices {
		result[i] = Device{
			ID:         d.ID,
			Name:       d.Name,
			Authorized: d.Authorized,
			Tags:       d.Tags,
		}
	}
	return result, nil
}

func (c *tailscaleDevicesClient) SetTags(ctx context.Context, deviceID string, tags []string) error {
	return c.client.Devices().SetTags(ctx, deviceID, tags)
}

func loadConfig() (Config, error) {
	tailnet := os.Getenv("TAILSCALE_TAILNET")
	if tailnet == "" {
		return Config{}, errors.New("TAILSCALE_TAILNET is required")
	}

	apiKey := os.Getenv("TAILSCALE_API_KEY")
	if apiKey == "" {
		return Config{}, errors.New("TAILSCALE_API_KEY is required")
	}

	tagsToApplyStr := os.Getenv("TAGS_TO_APPLY")
	if tagsToApplyStr == "" {
		return Config{}, errors.New("TAGS_TO_APPLY is required (e.g., tag:a,tag:b)")
	}
	var tagsToApply []string
	for _, tag := range strings.Split(tagsToApplyStr, ",") {
		if t := strings.TrimSpace(tag); t != "" {
			tagsToApply = append(tagsToApply, t)
		}
	}

	httpPort := os.Getenv("HTTP_PORT")
	if httpPort == "" {
		httpPort = "8080"
	}

	return Config{
		Tailnet:     tailnet,
		APIKey:      apiKey,
		TagsToApply: tagsToApply,
		HTTPPort:    httpPort,
	}, nil
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := loadConfig()
	if err != nil {
		slog.Error("Failed to load config", "error", err)
		os.Exit(1)
	}

	client := &tailscaleDevicesClient{
		client: &tsclient.Client{
			Tailnet: cfg.Tailnet,
			APIKey:  cfg.APIKey,
		},
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	mux := http.NewServeMux()

	// GET /healthz - Health check endpoint for Kubernetes probes.
	// Returns 200 OK if the server is running.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	// GET /pending-devices - Returns a list of Tailscale devices that are
	// authorized but have no tags assigned.
	// Response: {"pending_devices": [{"id": "...", "name": "..."}]}
	mux.HandleFunc("GET /pending-devices", func(w http.ResponseWriter, r *http.Request) {
		slog.Info("Getting pending devices")
		pending, err := getPendingDevices(r.Context(), client)
		if err != nil {
			slog.Error("Failed to get pending devices", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(PendingDevicesResponse{PendingDevices: pending})
	})

	// POST /approve/{deviceID} - Approves a device by applying the configured tags.
	// Returns 200 OK on success, 500 on failure.
	mux.HandleFunc("POST /approve/{deviceID}", func(w http.ResponseWriter, r *http.Request) {
		deviceID := r.PathValue("deviceID")
		slog.Info("Approve requested", "deviceID", deviceID)

		_, err := withRetry(r.Context(), func() (struct{}, error) {
			return struct{}{}, client.SetTags(r.Context(), deviceID, cfg.TagsToApply)
		})
		if err != nil {
			slog.Error("Failed to set tags", "deviceID", deviceID, "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		slog.Info("Approved device", "deviceID", deviceID, "tags", cfg.TagsToApply)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	// POST /decline/{deviceID} - Declines a device. Currently only logs the action.
	// Returns 200 OK.
	mux.HandleFunc("POST /decline/{deviceID}", func(w http.ResponseWriter, r *http.Request) {
		deviceID := r.PathValue("deviceID")
		slog.Info("Device declined", "deviceID", deviceID)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	server := &http.Server{Addr: ":" + cfg.HTTPPort, Handler: mux}

	slog.Info("Starting API server",
		"tailnet", cfg.Tailnet,
		"tags", cfg.TagsToApply,
		"port", cfg.HTTPPort,
	)

	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("HTTP server error", "error", err)
		}
	}()

	<-ctx.Done()
	slog.Info("Shutting down")
	server.Shutdown(context.Background())
}

func getPendingDevices(ctx context.Context, client DevicesClient) ([]PendingDevice, error) {
	devices, err := withRetry(ctx, func() ([]Device, error) {
		return client.List(ctx)
	})
	if err != nil {
		return nil, err
	}

	var pending []PendingDevice
	for _, device := range devices {
		if !device.Authorized {
			continue
		}

		if len(device.Tags) > 0 {
			continue
		}

		pending = append(pending, PendingDevice{
			ID:   device.ID,
			Name: device.Name,
		})
	}

	return pending, nil
}

func withRetry[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	var zero T
	maxRetries := 5
	backoff := 1 * time.Second

	for i := 0; i < maxRetries; i++ {
		result, err := fn()
		if err == nil {
			return result, nil
		}

		if i == maxRetries-1 {
			return zero, err
		}

		slog.Warn("Request failed, retrying", "attempt", i+1, "backoff", backoff, "error", err)

		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-time.After(backoff):
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
	}

	return zero, errors.New("max retries exceeded")
}
