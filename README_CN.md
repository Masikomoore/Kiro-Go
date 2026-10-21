# Kiro-Go

[![Go Version](https://img.shields.io/badge/Go-1.21+-00ADD8?style=flat&logo=go)](https://go.dev/)
[![Docker](https://img.shields.io/badge/Docker-Ready-2496ED?style=flat&logo=docker)](https://www.docker.com/)
[![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

将 Kiro 账号转换为 Anthropic / OpenAI 兼容的 API 服务。这个版本基于 [Quorinex/Kiro-Go](https://github.com/Quorinex/Kiro-Go)（MIT），在 [xclis.ai](https://xclis.ai) 的生产流量上长期打磨：多账号、多出口 IP、长对话、各种第三方客户端，账单与 Kiro 实际扣费对得上。

[English](README.md) | 中文 | [Tiếng Việt](README_VI.md)

如果这个项目帮到了你，欢迎点个 Star 支持一下。

## 你是不是也遇到过这些问题？

- 同一个对话每轮都换账号，Kiro 的缓存次次失效，credits 烧得飞快？
- WorkBuddy、Codex、各家 SDK 经网关接入，账单里永远没有缓存命中？
- effort=max 的长推理跑到 10 分钟被掐断，或者推理完一个字没回、credits 照扣？
- 带截图的长对话一过 900 KB 就丢历史，模型突然"失忆"？
- 想让不同账号走不同的出口 IP，连登录都从指定 IP 发起？
- 账号一多，面板里分不清谁走哪个代理、谁快没额度了？

这些都是这个 fork 改掉的问题。

## 相比上游改了什么

### 省钱：缓存和计费对齐 Kiro 的实际扣费

| 痛点 | 上游 | 本 fork |
|---|---|---|
| 对话在账号间轮换，Kiro 前缀缓存总失效 | 每个请求轮询换账号 | 会话粘性：同一对话固定在一个账号上；可选 sticky / round_robin / priority 三种调度和账号优先级。实测同一对话首字节 12.2 s → 3.9 s，单轮 credits 3.46 → 约 1.1 |
| 客户端看不到缓存命中，按全价计费 | 只在有 `cache_control` 时粗略模拟 | 按真正发给 Kiro 的内容（转换、截断之后）模拟 Anthropic prompt cache，cache read / cache creation 跟 Kiro 实际收费一致；不带 `cache_control` 的第三方客户端也有隐式缓存；按会话 / API Key / 账号隔离，重启不丢 |
| 计费口径不准 | 推理不计入输出、web_search 只算最后一轮 | 推理计入输出；web_search 多轮循环每轮的输入和输出都计；流在出字前失败不计费 |
| 工具调用每轮重复付费 | 上一轮工具调用被压平成文本，缓存在那里断开 | 可选结构化工具历史，工具调用保持原样，后续回合读缓存 |

### 稳定：长推理、长对话不再被打断

- **长推理：** 用"2 分钟没数据才断开"的空闲超时代替固定的总时限；effort=max 自动降为 xhigh；Kiro 已计费却只推理不回答的回合，按 `max_tokens` 正常结束并计费，不再返回 503 让客户端无限重试。
- **大上下文：** 请求上限从固定 900 KB 改为可调，默认 2 MB；当前消息本身超限时不再白白丢历史；截断位置跨回合保持稳定，不反复打破缓存；图片去重，大图可选缩放。
- **故障转移：** 区分客户端错误、限流、配额、凭证失效和临时故障，不误罚账号，并正确返回 400 / 429 / 503。
- **流式输出：** 静默生成期间发送 SSE ping 保活；检查流的完整性，被截断的流不会伪装成正常结束。

### 多出口：每个账号可以绑定自己的代理 IP

- 保存多个代理（SOCKS5 / HTTP），起名字、一键测连通。
- 添加账号时选代理：登录、取令牌都从这个 IP 发出，之后的刷新和对话请求也走它。
- 改了代理地址，绑定它的账号自动跟着更新。

### 管理面板

- 账号按出口分组：分组可拖动排序，没有可用账号的组自动折叠。
- 拖动卡片即调整调度优先级；新加的账号自动排到组内第一。
- 大 / 中 / 小三档卡片；额度用完和已禁用的账号置灰，禁用但还有额度的账号保留彩色额度条；显示额度重置倒计时。
- 兼容性设置在面板里热加载，不用重启。

### 性能与兼容

- 每个请求的配置写入改为批量落盘（每次约 1.5–7 ms → 约 41 ns），请求体每个端点只编码一次，首字节更快。
- 支持 fable / mythos 和 1M 上下文；正确处理 messages 里的 system 角色和 `temperature: 0`；思考块签名；过滤泄漏到正文里的 tool_use XML；引号和代码块里的 `<thinking>` 不误判；回复里的 "Kiro" 可改写为 "Claude"。

全部兼容性开关的说明见英文文档的 [Compatibility and Prompt-Cache Settings](README.md#compatibility-and-prompt-cache-settings)。

## 功能特性

- Anthropic `/v1/messages`、OpenAI `/v1/chat/completions` 与 OpenAI `/v1/responses`
- 多账号池：会话粘性、三种调度模式、账号优先级、按出口 IP 分组
- 自动 Token 刷新、SSE 流式输出、Web 管理面板
- 多种认证方式：AWS Builder ID、IAM Identity Center (企业 SSO)、Microsoft 企业 SSO、SSO Token、本地缓存、凭证 JSON、Kiro API Key
- 模拟 Anthropic 提示缓存，用量与 Kiro 实际扣费对齐
- 用量追踪、账号导入导出、中英越三语
- 多个出站代理（SOCKS5 / HTTP），账号可单独绑定代理

## 快速开始

### Docker Compose（推荐）

```bash
git clone https://github.com/Masikomoore/Kiro-Go.git
cd Kiro-Go
mkdir -p data
docker-compose up -d
```

### Docker 运行

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

> `ghcr.io/quorinex/kiro-go` 是上游镜像，不包含本 fork 的改动。

### 源码编译

```bash
git clone https://github.com/Masikomoore/Kiro-Go.git
cd Kiro-Go
go build -o kiro-go .
./kiro-go
```

### 部署到 Zeabur

仓库已包含 `Dockerfile`，可直接在 Zeabur 上构建运行。

**方式一：面板一键部署**

1. Fork 本仓库到你的 GitHub 账号。
2. 在 Zeabur 新建服务，选择 **Deploy from GitHub**，绑定刚才 fork 的仓库。
3. Zeabur 自动识别 `Dockerfile` 并完成构建。
4. 在 **Networking** 标签暴露端口 `8080` 并绑定域名。
5. 在 **Variables** 标签至少设置 `ADMIN_PASSWORD`（管理面板密码）。
6. 如需持久化账号 / 配置，挂载 Volume 到 `/app/data`。

**方式二：CLI 部署**

```bash
npm i -g zeabur
zeabur auth login
zeabur deploy
```

> 命令需在项目根目录执行。CLI 会生成 `.zeabur/context.json` 记录目标 project / service，包含个人 ID，请勿提交。

部署完成后访问 `https://<你的域名>/admin` 登录管理面板。

首次运行会在 `data/config.json` 自动生成配置，挂载 `/app/data` 以持久化。默认管理密码为 `changeme`，生产环境请务必通过 `ADMIN_PASSWORD` 环境变量或在管理面板中修改。

## 使用方法

访问 `http://localhost:8080/admin` 登录、添加账号，然后调用 API：

```bash
# Claude
curl http://localhost:8080/v1/messages \
  -H "Content-Type: application/json" \
  -H "anthropic-version: 2023-06-01" \
  -d '{"model":"claude-sonnet-4.5","max_tokens":1024,"messages":[{"role":"user","content":"你好！"}]}'

# OpenAI
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer any" \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"你好！"}]}'
```

### 添加 Kiro API Key 账号

管理面板「添加账号」可选择 **API Key**，填写 `ksk_...`（也支持 `ksk_...|region`）。

也可通过凭证导入接口添加：

```bash
curl -X POST http://localhost:8080/admin/api/auth/credentials \
  -H "Content-Type: application/json" \
  -H "Cookie: <admin-session>" \
  -d '{"kiroApiKey":"ksk_your_key|us-east-1","authMethod":"api_key","nickname":"cli-key"}'
```

API Key 账号会走 Kiro CLI runtime（`https://runtime.{region}.kiro.dev/`），请求头带 `tokentype: API_KEY`，无需 OAuth 刷新，也不使用 `profileArn`。

## 思考模式

在模型名后加后缀（默认 `-thinking`）即可启用，例如 `claude-sonnet-4.5-thinking`。Claude 兼容请求如果带有顶层 `thinking` 配置，例如 `{"type":"enabled","budget_tokens":2048}` 或 `{"type":"adaptive"}`，也会自动启用 thinking 模式。输出格式可在管理面板「设置 - Thinking 模式」中配置。

## 出站代理

可在管理面板「设置 - 出站代理设置」中配置代理。支持 SOCKS5 和 HTTP 代理。

设置保存后即时生效，无需重启服务。

## 环境变量

| 变量 | 说明 | 默认值 |
|-----|------|-------|
| `CONFIG_PATH` | 配置文件路径 | `data/config.json` |
| `ADMIN_PASSWORD` | 管理面板密码（覆盖配置文件） | - |

## 参与贡献

欢迎友好交流。遇到问题时，建议先让 Claude Code、Codex 等工具帮忙排查一下，大部分问题都能自己解决。如果能直接提个 PR 就更好了。

## 赞助商

- [xclis.ai — X全球中转](https://xclis.ai)

## 免责声明

本项目仅供学习和研究目的使用，与 Amazon、AWS 或 Kiro 没有任何关联。用户需自行确保使用行为符合所有适用的服务条款和法律法规，使用风险自负。

## 许可证

[MIT](LICENSE)
