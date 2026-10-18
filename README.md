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
| `schedulingMode` | `sticky` | Account choice per request. `sticky`: a conversation (Claude Code session, else API key) keeps its account so Kiro's per-account prompt cache hits; it moves on cooldown or failure, and new conversations rotate. `round_robin`: every request moves to the next account. `priority`: always the first ready account (kiro-RS default). Every mode serves the smallest account `priority` tier first. Also in Settings → Account scheduling |
| `accountThrottleCooldownMinutes` | `10` | Cooldown after a 429 "suspicious activity" response for that account |
| `structuredToolHistory` | `false` | Experimental: keep tool calls structured in history with placeholder tool specs |
| `normalizeKiroToolParams` | `false` | Experimental: rename Kiro-style arguments (`path`, `oldStr`, ...) on Claude Code tools |
| `imageDownscale` | `false` | Experimental: shrink PNG/JPEG over 400KB or 1568px |
| `builderIdPlaceholderArn` | `off` | `on_demand` or `always`: placeholder profileArn for Builder ID/social accounts |
| `promptCacheMode` | `full` | Anthropic only. `read_only` reports cache reads but no cache creation; `off` reports no cache fields |
| `simulateAnthropicCache` | `true` | Anthropic `/v1/messages` simulated cache. Off omits cache usage fields. Applies on the next request |
| `simulateOpenAICache` | `false` | OpenAI Chat Completions and Responses simulated cache reads (`cached_tokens` inside the prompt total, no cache-write charge). Applies on the next request |
| `promptCacheScope` | `auto` | `auto`: Claude Code session, then API key, then account; `account`: per upstream account |
| `promptCacheMaxEntries` | `20000` | LRU cap of the simulated prompt cache |

Kiro returns no cache token counts, so prompt-cache usage is simulated. Anthropic uses `cache_control` prefixes. OpenAI treats the system prompt, tools, and each completed message as a prefix and reports only cache reads. Neither switch changes Kiro credits. Input, cache creation, and cache read on the Anthropic path always add up to the real input total. The simulated cache is saved to `prompt_cache.json` next to `config.json`. The admin panel under Settings saves these two switches without a restart.

## Outbound Proxy

For users in restricted network regions, configure an outbound proxy in the admin panel under **Settings - Outbound Proxy Settings**. Supports SOCKS5 and HTTP proxies.

The setting takes effect immediately without restarting.

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
