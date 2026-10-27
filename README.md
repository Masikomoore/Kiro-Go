# Kiro-Go

[![Go Version](https://img.shields.io/badge/Go-1.21+-00ADD8?style=flat&logo=go)](https://go.dev/)
[![Docker](https://img.shields.io/badge/Docker-Ready-2496ED?style=flat&logo=docker)](https://www.docker.com/)
[![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

Turn Kiro accounts into an Anthropic / OpenAI compatible API. This fork of [Quorinex/Kiro-Go](https://github.com/Quorinex/Kiro-Go) (MIT) has been hardened on production traffic at [xclis.ai](https://xclis.ai): many accounts, several egress IPs, long conversations, and all kinds of third-party clients, with usage that matches what Kiro actually bills.

[English](README.md) | [中文](README_CN.md) | [Tiếng Việt](README_VI.md)

If this project helps you, a Star would mean a lot.

## Sound familiar?

- A conversation lands on a different account every turn, so Kiro's prompt cache never hits and credits disappear?
- WorkBuddy, Codex, or an SDK behind a gateway never shows a single cache hit on the bill?
- An effort=max turn gets cut off at the 10-minute mark, or reasons for ten minutes, answers nothing, and still burns credits?
- A long conversation with screenshots crosses 900 KB and the model suddenly forgets everything?
- You want each account on its own egress IP, including the login itself?
- With a dozen accounts, you can't tell in the panel which one uses which proxy or which one is nearly out of credits?

This fork fixes all of these.

## What changed compared with upstream

### Save money: cache and usage that match Kiro's real billing

| Problem | Upstream | This fork |
|---|---|---|
| Conversations hop between accounts and lose Kiro's prefix cache | Every request rotates to the next account | Session affinity keeps a conversation on one account; sticky / round_robin / priority scheduling and per-account priority. Measured on one conversation: first byte 12.2 s → 3.9 s, credits per turn 3.46 → about 1.1 |
| Clients never see cache hits and pay full price | Simulates only when the request carries `cache_control`, from the client's own messages | Simulates the Anthropic prompt cache from what is actually sent to Kiro (after conversion and truncation), so cache read / creation track Kiro's charges; implicit caching for third-party clients without `cache_control`; scoped per session / API key / account and kept across restarts |
| Usage is miscounted | Reasoning not counted as output; web_search billed for the last round only | Reasoning counts as output; every web_search round bills its input and output; streams that fail before any output bill nothing |
| Tool turns are paid for twice | The previous tool turn is flattened to text, breaking the cache there | Optional structured tool history keeps tool calls intact so later turns read the cache |

### Stay up: long reasoning and long conversations keep working

- **Long reasoning:** a 2-minute idle timeout replaces the fixed total deadline; effort=max is sent as xhigh; a turn Kiro billed but answered with reasoning only ends as `max_tokens` and is billed, instead of a 503 that clients retry forever.
- **Large contexts:** the request limit is configurable (default 2 MB, was a fixed 900 KB); history is no longer dropped when the current message alone is over the limit; the truncation point stays stable across turns so it doesn't keep breaking the cache; images are de-duplicated and can be downscaled.
- **Failover:** client errors, rate limits, quota, invalid credentials and transient faults are told apart, so accounts aren't penalized by mistake, and clients get the right 400 / 429 / 503.
- **Streaming:** SSE pings keep silent generations alive; truncated streams are reported as errors, never as a clean finish.

### Multiple egress IPs: every account can use its own proxy

- Save several proxies (SOCKS5 / HTTP), name them, and test them with one click.
- Pick a proxy when adding an account: login and token requests go out from that IP, and so do later refreshes and chat requests.
- Edit a proxy's address and the accounts bound to it follow.

### Admin panel

- Accounts are grouped by egress; drag groups to reorder them; groups without a usable account start collapsed.
- Drag cards to set scheduling priority; a newly added account goes first in its group.
- Large / medium / small cards; out-of-credits and disabled accounts are greyed out, while a disabled account with credits left keeps a coloured quota bar; quota reset countdown.
- Compatibility switches apply from the panel without a restart.

### Performance and compatibility

- Per-request config writes are batched (about 1.5–7 ms → about 41 ns per request); the request body is encoded once per endpoint; faster first byte.
- fable / mythos models and the 1M context window; system turns inside messages and `temperature: 0` handled correctly; signed thinking blocks; leaked tool_use XML stripped from answers; `<thinking>` inside quotes or code fences left alone; "Kiro" in replies can be rewritten to "Claude".

All switches are described under [Compatibility and Prompt-Cache Settings](#compatibility-and-prompt-cache-settings). Every change by version: [CHANGELOG.md](CHANGELOG.md).

## Features

- Anthropic `/v1/messages`, OpenAI `/v1/chat/completions` & OpenAI `/v1/responses`
- Multi-account pool: session affinity, three scheduling modes, per-account priority, groups by egress IP
- Auto token refresh, SSE streaming, Web admin panel
- Multiple auth: AWS Builder ID, IAM Identity Center (Enterprise SSO), Microsoft Enterprise SSO, SSO Token, local cache, credentials JSON, Kiro API Key
- Simulated Anthropic prompt cache, with usage that matches Kiro's billing
- Usage tracking, account import/export, i18n (CN / EN / VI)
- Several outbound proxies (SOCKS5 / HTTP), with a proxy per account

## Quick Start

### Docker Compose (Recommended)

```bash
git clone https://github.com/Masikomoore/Kiro-Go.git
cd Kiro-Go
mkdir -p data
docker-compose up -d
```

### Docker Run

```bash
git clone https://github.com/Masikomoore/Kiro-Go.git
cd Kiro-Go
docker build -t kiro-go .
docker run -d \
  --name kiro-go \
  -p 8080:8080 \
  -e ADMIN_PASSWORD=your_secure_password \
  -v /path/to/data:/app/data \
  --restart unless-stopped \
  kiro-go
```

> `ghcr.io/quorinex/kiro-go` is the upstream image and does not include this fork's changes.

### Build from Source

```bash
git clone https://github.com/Masikomoore/Kiro-Go.git
cd Kiro-Go
go build -o kiro-go .
./kiro-go
```

### Deploy on Zeabur

The repo already includes a `Dockerfile`, so it builds and runs on Zeabur out of the box.

**Option 1: Dashboard (one-click)**

1. Fork this repo to your GitHub account.
2. In Zeabur, create a new service and choose **Deploy from GitHub**, then select your fork.
3. Zeabur auto-detects the `Dockerfile` and builds the image.
4. In the **Networking** tab, expose port `8080` and bind a domain.
5. In the **Variables** tab, set at least `ADMIN_PASSWORD` (admin panel password).
6. Mount a Volume at `/app/data` if you want accounts / config to survive redeploys.

**Option 2: CLI**

```bash
npm i -g zeabur
zeabur auth login
zeabur deploy
```

> Run the commands from the project root. The CLI writes `.zeabur/context.json` to remember the target project / service — it contains personal IDs, so don't commit it.

Once the service is up, open `https://<your-domain>/admin` to log in.

Config is auto-created at `data/config.json`. Mount `/app/data` for persistence. The default admin password is `changeme` — override it via the `ADMIN_PASSWORD` env var or change it in the admin panel before going to production.

## Usage

Open `http://localhost:8080/admin`, log in, add accounts, then call the API:

```bash
# Claude
curl http://localhost:8080/v1/messages \
  -H "Content-Type: application/json" \
  -H "anthropic-version: 2023-06-01" \
  -d '{"model":"claude-sonnet-4.5","max_tokens":1024,"messages":[{"role":"user","content":"Hello!"}]}'

# OpenAI
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer any" \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"Hello!"}]}'
```

### Add a Kiro API Key account

In the admin panel, choose **API Key** when adding an account and paste `ksk_...` (or `ksk_...|region`).

You can also import via the credentials API:

```bash
curl -X POST http://localhost:8080/admin/api/auth/credentials \
  -H "Content-Type: application/json" \
  -H "Cookie: <admin-session>" \
  -d '{"kiroApiKey":"ksk_your_key|us-east-1","authMethod":"api_key","nickname":"cli-key"}'
```

API Key accounts call the Kiro CLI runtime (`https://runtime.{region}.kiro.dev/`) with `tokentype: API_KEY`. They skip OAuth refresh and do not use `profileArn`.

## Thinking Mode

Append a suffix (default `-thinking`) to the model name, e.g. `claude-sonnet-4.5-thinking`. Claude-compatible requests that include a top-level `thinking` config such as `{"type":"enabled","budget_tokens":2048}` or `{"type":"adaptive"}` also enable thinking mode automatically. Configure output format in the admin panel under Settings - Thinking Mode.

Claude 4.6+, 5.x, and Fable models receive thinking through `additionalModelRequestFields.output_config.effort` (from `output_config.effort`, `budget_tokens`, or OpenAI `reasoning_effort`) instead of prompt tags.

## Compatibility and Prompt-Cache Settings

The `compat` section of `config.json` (also `GET`/`POST /admin/api/settings` with `{"compat": {...}}`) holds these switches. Omitted keys use the default.

| Key | Default | Effect |
|-----|---------|--------|
| `nativeReasoningEffort` | `true` | Send `output_config.effort` to models that support it |
| `toolChunkingHints` | `true` | Tell the model to split large Write/Edit/Bash payloads, which Kiro can truncate |
| `thinkingSignature` | `true` | Add a placeholder signature to thinking blocks for SDK clients |
| `toolUseXmlFilter` | `true` | Strip `<tool_use>` XML leaked into answer text |
| `imageDedup` | `true` | Send each identical image once |
| `lowEffortWithoutThinking` | `true` | When the client did not ask for thinking, send `effort: low` to models that take `output_config.effort`. Kiro always reasons on them (it rejects `thinking: disabled`) and bills it, so this asks for the least. Small probes showed no clear saving yet. Off: send nothing, so Kiro uses its default |
| `capMaxEffort` | `true` | Send `effort: xhigh` instead of `max` to models that take `xhigh`. On Kiro, `max` on large prompts reasoned ~630 s, was billed ~14.8 credits and ended without an answer. Off: send `max` as asked |
| `maxPayloadKB` | `2048` | Kiro request text size (KB, 256–8192; image data not counted) above which history text is compacted, then old turns dropped. Kiro's limit counts text, not images: requests with 3–5.6 MB of history images passed. Images have their own 6 MB cap; above it old images are dropped, at a point kept per conversation. Lower it if Kiro answers "Input is too long." |
| `schedulingMode` | `sticky` | Account choice per request. `sticky`: a conversation (Claude Code session, else API key) keeps its account so Kiro's per-account prompt cache hits; it moves on cooldown or failure, and new conversations rotate. `round_robin`: every request moves to the next account. `priority`: always the first ready account (kiro-RS default). Every mode serves the smallest account `priority` tier first. Also in Settings → Account scheduling |
| `accountThrottleCooldownMinutes` | `10` | Cooldown after a 429 "suspicious activity" response for that account |
| `upstreamConnectTimeoutSeconds` | `15` | Seconds one Kiro attempt may take to connect (through its proxy) before failing over to another account (5–60). Connected requests are still governed by the 2-minute stall timer |
| `proxyDownSeconds` | `120` | After a proxy fails to connect, accounts behind it are skipped for this long (30–1800). A background probe clears the mark as soon as the proxy answers again |
| `structuredToolHistory` | `false` | Experimental: keep tool calls structured in history with placeholder tool specs |
| `normalizeKiroToolParams` | `false` | Experimental: rename Kiro-style arguments (`path`, `oldStr`, ...) on Claude Code tools |
| `imageDownscale` | `false` | Experimental: shrink PNG/JPEG over 400KB or 1568px |
| `builderIdPlaceholderArn` | `off` | `on_demand` or `always`: placeholder profileArn for Builder ID/social accounts |
| `promptCacheMode` | `full` | Anthropic only. `read_only` reports cache reads but no cache creation; `off` reports no cache fields |
| `simulateAnthropicCache` | `true` | Anthropic `/v1/messages` simulated cache. Off omits cache usage fields. Applies on the next request |
| `implicitAnthropicCache` | `true` | Also simulate `/v1/messages` requests that carry no `cache_control` (for example WorkBuddy through Sub2API's Chat Completions → Anthropic conversion) |
| `cliEndpointFirst` | `false` | Experimental: OAuth accounts try the Kiro CLI runtime first and fall back to the IDE endpoints on any error. A single request can ask for the same with the header `X-Kiro-Go-Endpoint: cli` |
| `promptCacheScope` | `auto` | Web-search paths only: `auto` scopes by Claude Code session, then API key, then account; `account` by upstream account. `/v1/messages` always scopes by upstream account, like Kiro's own cache |
| `promptCacheMaxEntries` | `20000` | LRU cap of the simulated prompt cache |

Kiro caches prompt prefixes per account (a repeated prefix costs roughly a third of the credits) but returns no cache token counts on either the IDE endpoint or the CLI runtime, so prompt-cache usage on `/v1/messages` is simulated. The simulation hashes the history actually sent to Kiro, after conversion and truncation, with a boundary after every history entry; the current message is never counted as cached. It is keyed by upstream account, so a turn that changes accounts, or whose early history kiro-go changed, reports creation as Kiro bills it. Requests without `cache_control` are simulated only when `implicitAnthropicCache` is on.

Requests over the ~900 KiB upstream limit drop their oldest turns. The cut point is remembered per conversation and reused while the payload fits; when it no longer fits, the new cut leaves 20% headroom. This keeps the history prefix byte-stable across turns so Kiro's cache keeps hitting.

A conversation keeps its upstream account (`schedulingMode: sticky`) by Claude Code session, else API key, else the Kiro conversation ID derived from the system prompt and first user message.

OpenAI `/v1/chat/completions` and `/v1/responses` report no cache fields. The switches do not change Kiro credits. Input, cache creation, and cache read always add up to the real input total. The simulated cache is saved to `prompt_cache.json` next to `config.json`. The admin panel under Settings saves these switches without a restart.

## Outbound Proxy

For users in restricted network regions, configure an outbound proxy in the admin panel under **Settings - Outbound Proxy Settings**. Supports SOCKS5 and HTTP proxies.

You can save several proxies by name and pick one as the global proxy. Accounts without their own proxy use the global one. An account's own proxy can be picked from the saved list in its detail panel. Accounts that use a saved proxy show its name as a tag on their card, and renaming a proxy's address moves the accounts that use it to the new address.

When adding an account, you can pick a saved proxy (or the global default) for all of its login and token requests; the choice is saved as that account's proxy.

The account list groups cards by outbound proxy (default first, then saved proxies), with a remaining-credits total per group. Cards come in large, medium (no stats row), or small (one row) sizes, chosen in the toolbar and kept per browser. Drag a card's grip handle within its group to set the scheduling order: the group's usable accounts get priority 1, 2, and so on, and accounts that are out of credits or disabled sort to the end. Groups can be dragged by their header to change the order they are shown in, and a group with no usable accounts starts collapsed; this order is display only, and scheduling still uses the priority numbers alone.

**Test** sends one request to the Kiro host (`q.us-east-1.amazonaws.com`) through the proxy and shows the latency. Any HTTP status counts as connected. Testing an empty field checks the direct route.

The global proxy takes effect immediately without restarting.

If a proxy cannot be reached, the accounts behind it are skipped and requests move to accounts that connect directly or through other proxies, instead of waiting out each attempt. The proxy is marked down in Settings and in the account cards, probed every 30 seconds, and used again once it answers. Tune the timings with `upstreamConnectTimeoutSeconds` and `proxyDownSeconds` in the compatibility table.

## Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `CONFIG_PATH` | Config file path | `data/config.json` |
| `ADMIN_PASSWORD` | Admin panel password (overrides config) | - |

## Contributing

Friendly discussion is welcome. If you run into issues, try asking Claude Code, Codex, or similar tools for help first — most problems can be solved that way. PRs are even better.

## Sponsor

- [xclis.ai — X全球中转](https://xclis.ai)

## Disclaimer

For educational and research purposes only. Not affiliated with Amazon, AWS, or Kiro. Users are responsible for complying with applicable terms of service and laws. Use at your own risk.

## License

[MIT](LICENSE)
