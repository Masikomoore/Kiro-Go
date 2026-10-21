# Kiro-Go

[![Go Version](https://img.shields.io/badge/Go-1.21+-00ADD8?style=flat&logo=go)](https://go.dev/)
[![Docker](https://img.shields.io/badge/Docker-Ready-2496ED?style=flat&logo=docker)](https://www.docker.com/)
[![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

Convert Kiro accounts to OpenAI / Anthropic compatible API service.

[English](README.md) | [中文](README_CN.md) | [Tiếng Việt](README_VI.md)

If this project helps you, a Star would mean a lot.

## Features

- Anthropic `/v1/messages`, OpenAI `/v1/chat/completions` & OpenAI `/v1/responses`
- Multi-account pool with round-robin load balancing
- Auto token refresh, SSE streaming, Web admin panel
- Multiple auth: AWS Builder ID, IAM Identity Center (Enterprise SSO), Microsoft Enterprise SSO, SSO Token, local cache, credentials JSON, Kiro API Key
- Usage tracking, account import/export, i18n (CN / EN / VI)
- Support configuring outbound proxy (SOCKS5 / HTTP)

## Quick Start

### Docker Compose (Recommended)

```bash
git clone https://github.com/Quorinex/Kiro-Go.git
cd Kiro-Go
mkdir -p data
docker-compose up -d
```

### Docker Run

```bash
docker run -d \
  --name kiro-go \
  -p 8080:8080 \
  -e ADMIN_PASSWORD=your_secure_password \
  -v /path/to/data:/app/data \
  --restart unless-stopped \
  ghcr.io/quorinex/kiro-go:latest
```

### Build from Source

```bash
git clone https://github.com/Quorinex/Kiro-Go.git
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
| `maxPayloadKB` | `2048` | Kiro request size (KB, 256–8192) above which old images are dropped, history text compacted, then old turns dropped. Kiro accepted 1.06–1.6 MB requests while the old fixed 900 KB dropped history and cost full cache misses. Lower it if Kiro answers "Input is too long." |
| `schedulingMode` | `sticky` | Account choice per request. `sticky`: a conversation (Claude Code session, else API key) keeps its account so Kiro's per-account prompt cache hits; it moves on cooldown or failure, and new conversations rotate. `round_robin`: every request moves to the next account. `priority`: always the first ready account (kiro-RS default). Every mode serves the smallest account `priority` tier first. Also in Settings → Account scheduling |
| `accountThrottleCooldownMinutes` | `10` | Cooldown after a 429 "suspicious activity" response for that account |
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

**Test** sends one request to the Kiro host (`q.us-east-1.amazonaws.com`) through the proxy and shows the latency. Any HTTP status counts as connected. Testing an empty field checks the direct route.

The global proxy takes effect immediately without restarting.

## Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `CONFIG_PATH` | Config file path | `data/config.json` |
| `ADMIN_PASSWORD` | Admin panel password (overrides config) | - |

## Contributing

Friendly discussion is welcome. If you run into issues, try asking Claude Code, Codex, or similar tools for help first — most problems can be solved that way. PRs are even better.

## Friend Links

- [LINUX DO](https://linux.do)

## Disclaimer

For educational and research purposes only. Not affiliated with Amazon, AWS, or Kiro. Users are responsible for complying with applicable terms of service and laws. Use at your own risk.

## License

[MIT](LICENSE)
