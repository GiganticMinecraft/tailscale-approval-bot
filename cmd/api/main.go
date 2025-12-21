package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"time"

	tsclient "github.com/tailscale/tailscale-client-go/v2"
)

type Config struct {
	Tailnet  string
	APIKey   string
	HTTPPort string
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

type TagsResponse struct {
	Tags []string `json:"tags"`
}

type ApproveRequest struct {
	Tags []string `json:"tags"`
}

type DevicesClient interface {
	List(ctx context.Context) ([]Device, error)
	SetTags(ctx context.Context, deviceID string, tags []string) error
}

type PolicyClient interface {
	GetAvailableTags(ctx context.Context) ([]string, error)
}

type tailscaleClient struct {
	client *tsclient.Client
}

func (c *tailscaleClient) List(ctx context.Context) ([]Device, error) {
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

func (c *tailscaleClient) SetTags(ctx context.Context, deviceID string, tags []string) error {
	return c.client.Devices().SetTags(ctx, deviceID, tags)
}

func (c *tailscaleClient) GetAvailableTags(ctx context.Context) ([]string, error) {
	acl, err := c.client.PolicyFile().Get(ctx)
	if err != nil {
		return nil, err
	}

	var tags []string
	for tag := range acl.TagOwners {
		tags = append(tags, tag)
	}
	slices.Sort(tags)
	return tags, nil
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

	httpPort := os.Getenv("HTTP_PORT")
	if httpPort == "" {
		httpPort = "8080"
	}

	return Config{
		Tailnet:  tailnet,
		APIKey:   apiKey,
		HTTPPort: httpPort,
	}, nil
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := loadConfig()
	if err != nil {
		slog.Error("Failed to load config", "error", err)
		os.Exit(1)
	}

	client := &tailscaleClient{
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

	// GET /tags - Returns available tags from the Tailscale ACL policy.
	// Response: {"tags": ["tag:a", "tag:b"]}
	mux.HandleFunc("GET /tags", func(w http.ResponseWriter, r *http.Request) {
		tags, err := withRetry(r.Context(), func() ([]string, error) {
			return client.GetAvailableTags(r.Context())
		})
		if err != nil {
			slog.Error("Failed to get available tags", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(TagsResponse{Tags: tags})
	})

	// POST /approve/{deviceID} - Approves a device by applying the specified tags.
	// Request body: {"tags": ["tag:a", "tag:b"]}
	// Returns 200 OK on success, 400 on invalid request, 500 on failure.
	mux.HandleFunc("POST /approve/{deviceID}", func(w http.ResponseWriter, r *http.Request) {
		deviceID := r.PathValue("deviceID")

		var req ApproveRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			slog.Error("Failed to decode request body", "error", err)
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		if len(req.Tags) == 0 {
			http.Error(w, "at least one tag is required", http.StatusBadRequest)
			return
		}

		// Validate that all requested tags are in the available tags list
		availableTags, err := withRetry(r.Context(), func() ([]string, error) {
			return client.GetAvailableTags(r.Context())
		})
		if err != nil {
			slog.Error("Failed to get available tags for validation", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		availableSet := make(map[string]bool)
		for _, t := range availableTags {
			availableSet[t] = true
		}
		for _, t := range req.Tags {
			if !availableSet[t] {
				slog.Error("Invalid tag requested", "tag", t)
				http.Error(w, "invalid tag: "+t, http.StatusBadRequest)
				return
			}
		}

		slog.Info("Approve requested", "deviceID", deviceID, "tags", req.Tags)

		_, err = withRetry(r.Context(), func() (struct{}, error) {
			return struct{}{}, client.SetTags(r.Context(), deviceID, req.Tags)
		})
		if err != nil {
			slog.Error("Failed to set tags", "deviceID", deviceID, "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		slog.Info("Approved device", "deviceID", deviceID, "tags", req.Tags)
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
