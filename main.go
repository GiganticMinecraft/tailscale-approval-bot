package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	tsclient "github.com/tailscale/tailscale-client-go/v2"
)

var (
	devicesProcessed = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "tailscale_tag_controller_devices_processed_total",
		Help: "Total number of devices processed",
	})
	tagsApplied = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "tailscale_tag_controller_tags_applied_total",
		Help: "Total number of devices that had tags applied",
	})
	reconcileErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "tailscale_tag_controller_reconcile_errors_total",
		Help: "Total number of reconciliation errors",
	})
	reconcileDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "tailscale_tag_controller_reconcile_duration_seconds",
		Help:    "Duration of reconciliation loops",
		Buckets: prometheus.DefBuckets,
	})
)

func init() {
	prometheus.MustRegister(devicesProcessed, tagsApplied, reconcileErrors, reconcileDuration)
}

type Config struct {
	Tailnet      string
	APIKey       string
	TagsToApply  []string
	PollInterval time.Duration
	HTTPPort     string
}

type Device struct {
	ID         string
	Name       string
	Authorized bool
	Tags       []string
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

	pollIntervalStr := os.Getenv("POLL_INTERVAL")
	pollInterval := 30 * time.Second
	if pollIntervalStr != "" {
		d, err := time.ParseDuration(pollIntervalStr)
		if err != nil {
			slog.Warn("Invalid POLL_INTERVAL, using default", "default", pollInterval, "error", err)
		} else {
			pollInterval = d
		}
	}

	httpPort := os.Getenv("HTTP_PORT")
	if httpPort == "" {
		httpPort = "8080"
	}

	return Config{
		Tailnet:      tailnet,
		APIKey:       apiKey,
		TagsToApply:  tagsToApply,
		PollInterval: pollInterval,
		HTTPPort:     httpPort,
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

	// Start metrics and health server
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	mux.Handle("/metrics", promhttp.Handler())

	server := &http.Server{Addr: ":" + cfg.HTTPPort, Handler: mux}
	go func() {
		slog.Info("Starting HTTP server", "port", cfg.HTTPPort)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("Metrics server error", "error", err)
		}
	}()

	slog.Info("Starting Tailscale tag controller",
		"tailnet", cfg.Tailnet,
		"tags", cfg.TagsToApply,
		"pollInterval", cfg.PollInterval,
	)

	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()

	// Run immediately on start
	reconcile(ctx, client, cfg.TagsToApply)

	for {
		select {
		case <-ctx.Done():
			slog.Info("Shutting down")
			server.Shutdown(context.Background())
			return
		case <-ticker.C:
			reconcile(ctx, client, cfg.TagsToApply)
		}
	}
}

func reconcile(ctx context.Context, client DevicesClient, tagsToApply []string) {
	start := time.Now()
	defer func() {
		reconcileDuration.Observe(time.Since(start).Seconds())
	}()

	devices, err := withRetry(ctx, func() ([]Device, error) {
		return client.List(ctx)
	})
	if err != nil {
		slog.Error("Failed to list devices", "error", err)
		reconcileErrors.Inc()
		return
	}

	for _, device := range devices {
		devicesProcessed.Inc()

		if !device.Authorized {
			continue
		}

		if len(device.Tags) > 0 {
			continue
		}

		_, err := withRetry(ctx, func() (struct{}, error) {
			return struct{}{}, client.SetTags(ctx, device.ID, tagsToApply)
		})
		if err != nil {
			slog.Error("Failed to set tags",
				"device", device.Name,
				"deviceID", device.ID,
				"error", err,
			)
			reconcileErrors.Inc()
			continue
		}

		tagsApplied.Inc()
		slog.Info("Applied tags to device",
			"device", device.Name,
			"deviceID", device.ID,
			"tags", tagsToApply,
		)
	}
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

		// Check for rate limit (429)
		if strings.Contains(err.Error(), "429") || strings.Contains(err.Error(), "rate limit") {
			slog.Warn("Rate limited, waiting before retry", "attempt", i+1, "backoff", backoff)
		} else if i == maxRetries-1 {
			return zero, err
		} else {
			slog.Warn("Request failed, retrying", "attempt", i+1, "backoff", backoff, "error", err)
		}

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
