# tailscale-approval-bot

タグが付いていないTailscaleデバイスをDiscordで通知し、ボタンひとつで承認できるBot。

## アーキテクチャ

```
┌─────────────────────┐                      ┌─────────────────────┐
│         api         │                      │    discord-bot      │
│                     │ ← /pending-devices ─ │                     │
│ GET pending devices │                      │ /tailscale-approve  │
│                     │ ←── /approve/{id} ── │                     │
│ Apply tags          │                      │ [Approve] [Decline] │
└─────────────────────┘                      └─────────────────────┘
```

## フロー

1. Botが定期的にタグなしデバイスをチェック（または `/tailscale-approve` コマンドで手動実行）
2. タグなしデバイスが見つかったらDiscordに通知
   - 1-2台: Approve/Declineボタン付きメッセージ
   - 3台以上: Tailscale管理コンソールを確認するよう警告
3. ユーザーがApprove/Declineをクリック
4. BotがAPIを呼び出してタグを適用（またはDeclineをログ出力）

## コンポーネント

### API

| 環境変数 | 必須 | 説明 |
|---------|------|------|
| `TAILSCALE_TAILNET` | Yes | tailnet名 |
| `TAILSCALE_API_KEY` | Yes | Tailscale APIキー |
| `TAGS_TO_APPLY` | Yes | 適用するタグ（カンマ区切り、例: `tag:a,tag:b`） |
| `HTTP_PORT` | No | HTTPサーバーのポート（デフォルト: `8080`） |

#### エンドポイント

| パス | メソッド | 説明 |
|-----|---------|------|
| `/healthz` | GET | ヘルスチェック |
| `/pending-devices` | GET | タグなしデバイス一覧を取得 |
| `/approve/{deviceID}` | POST | デバイスにタグを適用 |
| `/decline/{deviceID}` | POST | デバイスを拒否（ログ出力のみ） |

### Discord Bot

| 環境変数 | 必須 | 説明 |
|---------|------|------|
| `DISCORD_BOT_TOKEN` | Yes | Discord Botトークン |
| `DISCORD_CHANNEL_ID` | Yes | 通知を送るチャンネルID |
| `API_URL` | No | APIサーバーのURL（デフォルト: `http://localhost:8080`） |
| `POLL_INTERVAL` | No | チェック間隔（デフォルト: `24h`） |

## Dockerイメージ

```bash
# API
docker build --target api -t api .

# Discord Bot
docker build --target discord -t discord .
```

## ライセンス

MIT
