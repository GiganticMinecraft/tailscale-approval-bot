# tailscale-tag-controller

A simple controller that automatically applies tags to authorized Tailscale devices.

## Overview

This controller periodically polls the Tailscale API and applies specified tags to all devices that have been authorized but don't yet have any tags.

## Configuration

| Environment Variable | Required | Description |
|---------------------|----------|-------------|
| `TAILSCALE_TAILNET` | Yes | Your tailnet name |
| `TAILSCALE_API_KEY` | Yes | Tailscale API key |
| `TAGS_TO_APPLY` | Yes | Comma-separated tags (e.g., `tag:a,tag:b`) |
| `POLL_INTERVAL` | No | Polling interval (default: `30s`) |
| `HTTP_PORT` | No | HTTP server port (default: `8080`) |

## Endpoints

| Path | Description |
|------|-------------|
| `/healthz` | Health check endpoint |
| `/metrics` | Prometheus metrics |

## Metrics

| Metric | Type | Description |
|--------|------|-------------|
| `tailscale_tag_controller_devices_processed_total` | Counter | Total devices processed |
| `tailscale_tag_controller_tags_applied_total` | Counter | Total devices that had tags applied |
| `tailscale_tag_controller_reconcile_errors_total` | Counter | Total reconciliation errors |
| `tailscale_tag_controller_reconcile_duration_seconds` | Histogram | Reconciliation loop duration |

## Usage

```bash
export TAILSCALE_TAILNET="your-tailnet"
export TAILSCALE_API_KEY="tskey-api-..."
export TAGS_TO_APPLY="tag:approved,tag:member"
export POLL_INTERVAL="1m"

./controller
```

## License

MIT
