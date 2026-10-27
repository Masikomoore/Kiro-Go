# Changelog

All notable changes of this fork ([Masikomoore/Kiro-Go](https://github.com/Masikomoore/Kiro-Go)) on top of upstream
[Quorinex/Kiro-Go](https://github.com/Quorinex/Kiro-Go). [中文](CHANGELOG_CN.md)

## Unreleased

### Billing and prompt cache
- **Changing system-prompt tail no longer starts a cold conversation every turn** (`d78085b`,
  `compat.stableSystemPrompt`, default on). When a client's system prompt differs from its previous one only in the
  last few KB, the shared prefix stays in history entry 0 and the conversation ID, and the changing tail goes with
  the current message. One client had been billed every turn cold at 370k–460k tokens.
- **History images are kept unless images alone pass 6 MB.** Kiro's input limit counts text, not image data (requests
  with 3–5.6 MB of history images passed, every image read), so `compat.maxPayloadKB` now applies to the payload
  without image data. Above the image cap old images are dropped at a point kept per conversation, which moves only
  when it must, down to half the cap. Removing an image made Kiro bill the whole prompt cold; image-heavy
  conversations used to lose images every 3 user turns at 2 MB.

### Reliability
- **Fast failover when an account's proxy is down** (`4d5a89a`). A 15 s connect timer per upstream attempt
  (`compat.upstreamConnectTimeoutSeconds`) catches an unreachable proxy instead of the 2-minute idle timer; the request
  moves straight to another account instead of trying every endpoint through the same dead proxy. A per-proxy breaker
  (`compat.proxyDownSeconds`, default 120 s) skips every account behind that proxy, including sticky conversations; a
  background probe every 30 s closes it as soon as the proxy answers again. Dial and TLS handshake timeouts (10 s) on
  the Kiro and auth transports. The admin panel marks a down proxy on its group, account tags and settings list.
  Prompted by a 44-minute outage on 2026-10-22 in which every request behind one SOCKS proxy hung for minutes.
- **web_search loop retries truncated rounds** (`915f211`). A round that streamed reasoning only, with no stop reason
  and no metering, is retried instead of being returned as an empty finished turn (and billed).

## 1.2.0 — 2026-10-21

### Billing and prompt cache
- Simulated Anthropic prompt cache computed from what is actually sent to Kiro (after conversion and truncation),
  scoped per session / API key / upstream account and persisted across restarts; implicit caching for clients that send
  no `cache_control`. Cache read / creation track what Kiro bills (`2a04f4e`, `b04b6e9`, `23a8580`, `34895c4`, `ac1fe5c`,
  `9aa4d02`, `5cc834c`, `e3bb758`).
- Usage counts Kiro's reasoning as output, also when the client did not ask for thinking (`9591e11`).
- web_search loop: every round's output and input are billed, each round with its own cache split (`7894cad`, `291526e`).
- A turn Kiro billed but answered with reasoning only ends as `max_tokens` and is billed, instead of a 503 that clients
  retried forever (`b1bd9f3`).
- Streams that fail before any output bill nothing; for clients without `cache_control` usage is reported in
  `message_delta` only (`f9b37e6`).

### Scheduling and accounts
- One upstream account per conversation (session affinity, with a conversation-ID fallback for third-party clients),
  scheduling modes `sticky` / `round_robin` / `priority` and per-account priority (`359e47d`, `eaca584`, `c2f7211`,
  `82c0b69`).
- Drag account cards to set priority; priority is the rank within an egress group; a newly added account goes first in
  its group (`367b2f0`, `6a61763`).
- Pick an outbound proxy when adding an account: login and token requests go out through it and the account is saved
  with it (`cf791d4`).

### Reliability
- Upstream errors classified before penalising accounts; correct 400 / 429 / 503 to clients (`b717ff4`).
- SSE pings during silent generations (`2da3daf`).
- 2-minute idle timeout instead of a fixed stream deadline (`e1cab79`, `c596aab`).
- Request size limit configurable, default 2 MB (`compat.maxPayloadKB`, was a fixed 900 KB); history is kept when the
  current message alone is over the limit; stable truncation point across turns (`1d4622d`, `9297637`, `23a8580`).
- `effort: max` sent as `xhigh` (`compat.capMaxEffort`) (`1d4622d`); `effort: low` when the client did not ask for
  thinking (`compat.lowEffortWithoutThinking`) (`5723fb2`).
- No per-request token refresh for accounts without a profile ARN (`a329d2b`).

### Proxies
- Several saved outbound proxies with names and a connectivity test; per-account proxy (`0f7014b`, `d243f49`).

### Admin panel
- Accounts grouped by egress proxy; groups can be dragged and collapse when they have no usable account (`367b2f0`,
  `df16dc8`).
- Large / medium / small cards; proxy tag on accounts; out-of-credits and disabled accounts greyed (a disabled account
  with credits left keeps a coloured quota bar); quota reset countdown; quota percentage computed from current / limit
  (`367b2f0`, `d243f49`, `df16dc8`).
- Compatibility settings editable without a restart (`4c2fd2f`).
- Update check and links point at this fork (`4080bd9`).

### Performance
- Per-request config writes batched (≈1.5–7 ms → ≈41 ns), upstream payload encoded once per endpoint, smaller
  answer-text holdback (`0a0d6ce`, `b71145b`, `518a132`).

### Compatibility
- fable / mythos models and the 1M context window; system turns inside messages; `temperature: 0`; signed thinking
  blocks; leaked tool_use XML stripped; quoted / fenced `<thinking>` left alone; native `output_config.effort`
  (`e4cd21e`, `aca4e7d`, `f4b9571`, `e2dc331`).
- Optional, default off: structured tool history, Kiro-style tool argument normalisation, image downscaling, placeholder
  profile ARN (`eb89d57`, `6fe76c2`, `4efa894`, `8fbaf06`). Image de-duplication and history shrinking before dropping
  turns (`1edbc29`).

### Diagnostics
- Per-call upstream timing, upstream usage events per attempt, per-conversation cache divergence, reasoning and
  cachePoint probe headers, experimental CLI runtime trial (`195f83b`, `4d291f6`, `e13a685`, `7918fcb`, `7ceac28`,
  `44a12be`, `c0cbeb8`).

## 1.1.7 — 2026-10-10
- Prompt-filter rules also rewrite "Kiro" in assistant text and reasoning (`c2453eb`).

## 1.1.6 — 2026-10-10
- A `meteringEvent` completes a turn that has no `metadataEvent` (`7e61355`).

## Base
- Forked from upstream Quorinex/Kiro-Go 1.1.5 (`f8f6071`).
