# tailscale-approval-bot

タグが付いていないTailscaleデバイスをDiscordで通知し、タグを選択して承認できるBot。

## アーキテクチャ

```
┌─────────────────────┐                      ┌─────────────────────┐
│         api         │                      │    discord-bot      │
│                     │ ← /pending-devices ─ │                     │
│ GET pending devices │                      │ /tailscale-approve  │
│                     │ ←──── /tags ──────── │                     │
│ GET available tags  │                      │ [Approve] [Decline] │
│                     │ ←── /approve/{id} ── │        ↓            │
│ Apply tags          │                      │ [Select tags...]    │
└─────────────────────┘                      └─────────────────────┘
```

## フロー

1. Botが定期的にタグなしデバイスをチェック（または `/tailscale-approve` コマンドで手動実行）
2. タグなしデバイスが見つかったらDiscordに通知
   - 1-2台: Approve/Declineボタン付きメッセージ
   - 3台以上: Tailscale管理コンソールを確認するよう警告
3. ユーザーがApproveをクリック
4. Tailscale ACLから取得したタグ一覧がドロップダウンで表示される
5. ユーザーがタグを選択（複数選択可）
6. BotがAPIを呼び出して選択したタグを適用

## コンポーネント

### API

| 環境変数 | 必須 | 説明 |
|---------|------|------|
| `TAILSCALE_TAILNET` | Yes | Tailnet ID |
| `TAILSCALE_API_KEY` | Yes | Tailscale APIキー |
| `HTTP_PORT` | No | HTTPサーバーのポート（デフォルト: `8080`） |

#### 必要なAPIキー権限

| スコープ | 用途 |
|---------|------|
| `devices:read` | デバイス一覧の取得 |
| `devices:write` | デバイスへのタグ適用 |
| `policy_file:read` | ACLからタグ一覧の取得 |

#### エンドポイント

| パス | メソッド | 説明 |
|-----|---------|------|
| `/healthz` | GET | ヘルスチェック |
| `/pending-devices` | GET | タグなしデバイス一覧を取得 |
| `/tags` | GET | 利用可能なタグ一覧を取得（ACLの`tagOwners`から） |
| `/approve/{deviceID}` | POST | デバイスに指定タグを適用（body: `{"tags": ["tag:a"]}`) |
| `/decline/{deviceID}` | POST | デバイスを拒否（ログ出力のみ） |

### Discord Bot

| 環境変数 | 必須 | 説明 |
|---------|------|------|
| `DISCORD_BOT_TOKEN` | Yes | Discord Botトークン |
| `DISCORD_CHANNEL_ID` | Yes | 通知を送るチャンネルID |
| `DISCORD_GUILD_ID` | No | サーバーID |
| `API_URL` | No | APIサーバーのURL（デフォルト: `http://localhost:8080`） |
| `POLL_INTERVAL` | No | チェック間隔（デフォルト: `24h`） |
| `MENTION_USER_IDS` | No | 自動通知時にメンションするユーザーID（カンマ区切り） |

#### 必要なBot権限

- View Channels
- Send Messages
- Read Message History

OAuth2スコープ: `bot`, `applications.commands`

## ライセンス

MIT
