# Microsoft Teams

cc-connect connects to Microsoft Teams as a Bot Framework bot. Unlike the
outbound-connection platforms (Slack socket mode, Feishu websocket), Teams
delivers messages by **POSTing to a public HTTPS webhook**, so this platform
requires a publicly reachable URL.

## How it works

- Azure Bot Service relays Teams messages to the connector's webhook
  (`/api/messages` by default) as Bot Framework activities.
- The connector validates each request's JWT, then forwards the message to the
  cc-connect engine.
- Replies are sent back through the Bot Connector REST API and stream as an
  **Adaptive Card**: a "working" card is posted immediately, edited in place as
  the answer grows, and finalized with the native "AI generated" label. The card
  renders uniformly in channels, group chats, and 1:1 (unlike the native
  `streamType` protocol, which is one-on-one only).

Engagement model: in a **channel or group chat**, an **@mention** of the bot
engages that reply thread; subsequent messages **in that thread** are then
followed without re-mentioning. A message that @mentions other people but not the
bot is treated as human-to-human side chatter and ignored. Engagement is
persisted under the cc-connect data dir (`<data_dir>/teams/<project>-engaged.json`),
so it survives a restart. In a **1:1 (personal) chat** the bot responds to every
message — Teams does not allow @mentioning a bot there.

## Prerequisites

- An **Azure Bot resource** (Azure Portal → "Azure Bot"), created as a
  **single-tenant** app. Note its **Microsoft App ID**, its **tenant (directory)
  ID**, and create a **client secret**. (Multi-tenant bots are not supported —
  Azure deprecated their creation after 2025-07-31.)
- The bot's **Microsoft Teams** channel enabled.
- A public HTTPS endpoint that routes to the connector's webhook port.

## Setup

1. **Create the Azure Bot** (single-tenant) and record the App ID, tenant ID, and client secret.
2. **Set the messaging endpoint** (Azure Bot → Configuration →
   "Messaging endpoint") to:

   ```
   https://<your-host><webhook_path>
   ```

   where `<webhook_path>` matches your config (default `/api/messages`).
3. **Enable the Teams channel** (Azure Bot → Channels → Microsoft Teams).
4. **Configure cc-connect** — add a Teams platform to your project:

   ```toml
   [[projects.platforms]]
   type = "teams"

   [projects.platforms.options]
   app_id = "00000000-0000-0000-0000-000000000000"  # Azure Bot App (client) ID
   app_password = "YOUR_CLIENT_SECRET"              # Azure Bot client secret
   tenant_id = "<your-tenant-id>"                   # required — AAD tenant that owns the bot (single-tenant)
   webhook_port = "3978"                            # local bind port
   webhook_path = "/api/messages"                   # must match the messaging endpoint path
   allow_from = "*"                                 # "*"/empty = all users in the tenant, or AAD object IDs
   session_scope = "thread"                         # "thread" (default) | "channel" | "user"
   card_update_interval_ms = 1500                   # streaming card edit throttle (ms)
   ```

   > **Access control:** the connector is **single-tenant** (`tenant_id` required), so only
   > users in that one organization can reach the bot. `allow_from = "*"` (the default) then
   > permits everyone in the tenant; set an AAD-object-ID allowlist to restrict to specific
   > people when you want a tighter boundary.

5. **Expose the webhook.** Put a reverse proxy / ingress with TLS in front of
   `webhook_port`, or use a tunnel (e.g. for local testing) so Azure Bot Service
   can reach `https://<your-host><webhook_path>`.
6. **Install the bot in Teams** (sideload an app manifest pointing at your bot,
   or add it from your org's catalog) and @mention it to start.

## Options

| Option | Required | Default | Description |
|--------|----------|---------|-------------|
| `app_id` | yes | — | Azure Bot App (client) ID; the inbound JWT audience |
| `app_password` | yes | — | Azure Bot client secret (outbound token) |
| `tenant_id` | **yes** | — | AAD tenant that owns the bot. Single-tenant only — Azure deprecated multi-tenant bot creation after 2025-07-31 |
| `webhook_port` | no | `3978` | Local port the webhook binds to |
| `webhook_path` | no | `/api/messages` | Path of the messaging endpoint |
| `allow_from` | no | `""` | Comma-separated AAD object IDs allowed to use the bot; `*` or empty = all users in the tenant |
| `session_scope` | no | `thread` | `thread` (one session per reply thread), `channel` (one shared session across the channel), `user` (one session per user within a thread) |
| `card_update_interval_ms` | no | `1500` | Streaming-card edit throttle in ms; Teams rate-limits edits to ~1/s |

## Connection type

Webhook (Bot Framework) — **a public HTTPS URL is required**. This is inherent
to the Bot Framework: Teams messages always route through Azure Bot Service,
which POSTs to your endpoint.

## Limitations (MVP)

- Replies stream as an Adaptive Card only. Plain text streaming, a `reply_format`
  toggle, and the native `streamType` 1:1 animation are deferred to follow-ups.
- Permission prompts render as plain text with numbered options (reply with a
  number or `yes`); interactive Adaptive Card buttons are deferred.
- No inbound images/files/audio.
- No cron/timer → Teams proactive messages (a conversation-reference store is
  needed to send without an incoming activity).
