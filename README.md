# 🪖 trooper

> **Local first. Cloud on demand.**  
> Trooper is a zero-config proxy that keeps your AI conversations alive. When your cloud LLM fails, it falls back to local Ollama — full conversation context intact. Your app never knows.

---

## Why Trooper

**Local first. Cloud on demand.**

Every other LLM proxy defaults to cloud-to-cloud routing. Trooper is built differently — your first fallback is always your local Ollama. Private. Your data never leaves your machine unless you explicitly choose it.

Need more cloud resilience? Add provider keys and Trooper extends the chain automatically. No YAML. No config files. Just keys.

```
Default:     Claude → Ollama          (private, always)
Opt-in:      Claude → Gemini → Ollama (cloud when you choose)
Full chain:  Claude → Gemini → OpenAI → Ollama
```

This is what separates Trooper from LiteLLM, Bifrost, and every other gateway — they're built for cloud teams. Trooper is built for developers who value simplicity and privacy first.

---

## How it works

```
Your App → http://localhost:3000 → Claude ✅
                                 → quota hit ⚡
                                 → Ollama (local) 🪖 full context intact
```

One environment variable swap and you're protected. Your app never changes.

---

## Demo

Start Trooper and watch the full chain in action:

```
2026/04/29 08:45:58 🏥 Auto recovery disabled — set AUTO_RECOVERY=true to enable
2026/04/29 08:45:58 🪖  Trooper proxy starting on http://localhost:3000
2026/04/29 08:45:58     Provider 1: claude
2026/04/29 08:45:58     Provider 2: gemini
2026/04/29 08:45:58     Provider 3: ollama
2026/04/29 08:45:58     Triggers : HTTP map[400:true 402:true 429:true 529:true]
2026/04/29 08:55:23 📥 POST /v1/messages (stream=false, session=abc-123)
2026/04/29 08:55:24 🔄 Trying provider: claude
2026/04/29 08:55:24 ⚠️  claude 400 — credit balance too low, trying next
2026/04/29 08:55:24 🔄 Trying provider: gemini
2026/04/29 08:55:25 ⚠️  gemini 429 — rate limited, retrying with backoff
2026/04/29 08:55:27 ⚠️  gemini still failing after backoff — trying next
2026/04/29 08:55:27 🔄 Trying provider: ollama
2026/04/29 08:55:27 🪖  Routing to local model: qwen2.5:3b
```

Response headers show exactly what happened:

```
X-Trooper-Provider: ollama
X-Trooper-Fallback-Count: 2
X-Trooper-Trigger: 429
```

Full conversation context preserved — Ollama picks up exactly where Claude left off:

**Turn 1** — sent to Claude, fell back to Ollama:
```bash
curl http://localhost:3000/v1/messages \
  -H "X-Session-ID: my-session" \
  -d '{"messages": [{"role": "user", "content": "my project deadline is Friday and I prefer TypeScript"}]}'
# Response: "Got it! I'll keep that in mind."
```

**Turn 2** — new request, same session:
```bash
curl http://localhost:3000/v1/messages \
  -H "X-Session-ID: my-session" \
  -d '{"messages": [{"role": "user", "content": "what do you know about me?"}]}'
# Response: "Your project deadline is Friday and you prefer TypeScript over JavaScript."
```

The app never knew Claude went down. 🪖

---

## Quickstart

### Docker (recommended)

```bash
git clone https://github.com/shouvik12/trooper
cd trooper

cp .env.example .env
# edit .env — set CLAUDE_API_KEY

docker compose up
```

### Local

```bash
# Prerequisites: Go 1.22+, Ollama running locally
ollama pull qwen2.5:3b
ollama serve

export CLAUDE_API_KEY=sk-ant-...
go run main.go providers.go
```

Trooper starts on `http://localhost:3000`.

> ⚠️ If no cloud provider key is set, Trooper will warn on startup:
> ```
> ⚠️  No cloud providers configured — set at least one of: CLAUDE_API_KEY, GEMINI_API_KEY, OPENAI_API_KEY
>     Trooper needs a cloud provider to fall back from.
> ```

> 💡 Enable auto recovery to silently route back when a provider recovers:
> ```bash
> AUTO_RECOVERY=true go run main.go providers.go
> ```

---

## Usage

Just change your base URL — nothing else:

**Python + Claude SDK:**
```python
import anthropic

client = anthropic.Anthropic(
    api_key="your-key",
    base_url="http://localhost:3000",  # 👈 only change
)
```

**Python + OpenAI SDK:**
```python
from openai import OpenAI

client = OpenAI(
    api_key="your-key",
    base_url="http://localhost:3000",  # 👈 only change
)
```

**curl:**
```bash
curl http://localhost:3000/v1/messages \
  -H "Content-Type: application/json" \
  -H "x-api-key: $CLAUDE_API_KEY" \
  -d '{
    "model": "claude-3-5-haiku-20241022",
    "max_tokens": 256,
    "messages": [{"role": "user", "content": "Hello!"}]
  }'
```

---

## Smart Chain

By default Trooper falls back to local Ollama — your conversations never leave your machine.

Add cloud provider keys to extend the chain:

```bash
# Default — Claude with local fallback
CLAUDE_API_KEY=sk-ant-...
# Chain: Claude → Ollama

# Add Gemini as cloud fallback before Ollama
CLAUDE_API_KEY=sk-ant-...
GEMINI_API_KEY=AIza...
# Chain: Claude → Gemini → Ollama

# Full cloud chain — Ollama always last
CLAUDE_API_KEY=sk-ant-...
GEMINI_API_KEY=AIza...
OPENAI_API_KEY=sk-...
# Chain: Claude → Gemini → OpenAI → Ollama

# Gemini only — no Claude
GEMINI_API_KEY=AIza...
# Chain: Gemini → Ollama
```

Ollama is always last. Always private. Always there.

---

## Context Preservation

Trooper maintains full conversation history server-side per session. When your cloud provider fails mid-conversation, Ollama picks up with complete context intact.

Pass `X-Session-ID` header to enable named session tracking:

```bash
# Turn 1 — tell Trooper something
curl http://localhost:3000/v1/messages \
  -H "Content-Type: application/json" \
  -H "X-Session-ID: dev-session-001" \
  -d '{
    "model": "claude-3-5-haiku-20241022",
    "max_tokens": 100,
    "messages": [{"role": "user", "content": "I am building a Go microservice with Postgres"}]
  }'

# Turn 2 — even after provider switch, context is preserved
curl http://localhost:3000/v1/messages \
  -H "Content-Type: application/json" \
  -H "X-Session-ID: dev-session-001" \
  -d '{
    "model": "claude-3-5-haiku-20241022",
    "max_tokens": 100,
    "messages": [{"role": "user", "content": "what stack am I using?"}]
  }'
# Response: "You are building a Go microservice with Postgres."
```

> 💡 No `X-Session-ID`? Trooper assigns a unique auto session per request — no context bleed between users.

---

## Smart Fallback

Trooper handles different failure modes intelligently:

| Status | Trooper action |
|---|---|
| `200 OK` | Pass through |
| `429 Rate Limited` | Retry with 2s backoff, then try next provider |
| `402 Payment Required` | Fall back immediately |
| `400 Credit Balance` | Detect credit error, fall back immediately |
| `401 Unauthorized` | Surface error — bad keys are never masked |
| `529 Overloaded` | Fall back immediately |
| Network error | Fall back immediately — 30s timeout per provider |

---

## Health Headers

Every response includes Trooper's routing decision:

```bash
curl http://localhost:3000/v1/messages ... -v 2>&1 | grep X-Trooper

# Claude served directly
X-Trooper-Provider: claude
X-Trooper-Fallback-Count: 0
X-Trooper-Trigger:

# Claude failed, Ollama caught it
X-Trooper-Provider: ollama
X-Trooper-Fallback-Count: 1
X-Trooper-Trigger: credit_balance

# Claude + Gemini failed, Ollama caught it
X-Trooper-Provider: ollama
X-Trooper-Fallback-Count: 2
X-Trooper-Trigger: 429
```

Log them, alert on them, build dashboards around them.

---

## Auto Recovery *(experimental)*

Trooper can automatically recover to the best available provider when it comes back online. Disabled by default — enable with a feature flag:

```bash
AUTO_RECOVERY=true go run main.go providers.go
```

When enabled, Trooper runs a background health check every 60 seconds. If a higher-priority provider recovers, Trooper silently routes back to it. Your app always gets the best available model without any intervention.

```
2026/04/29 09:00:00 🏥 Auto recovery enabled — checking every 60 seconds
2026/04/29 09:01:00 🏥 Health check running...
2026/04/29 09:01:01 🔄 Auto recovery — switching back to claude
```

> ⚠️ This feature is experimental. Enable it when you're comfortable with the behaviour in your environment.

---

## Configuration

| Variable | Default | Description |
|---|---|---|
| `CLAUDE_API_KEY` | *(optional)* | Anthropic API key — set at least one cloud provider key |
| `GEMINI_API_KEY` | *(optional)* | Google Gemini API key — enables Gemini in chain |
| `OPENAI_API_KEY` | *(optional)* | OpenAI API key — enables OpenAI in chain |
| `OLLAMA_MODEL` | `qwen2.5:3b` | Local model to fall back to |
| `FALLBACK_URL` | `http://localhost:11434/api/chat` | Local Ollama endpoint |
| `QUOTA_STATUS_CODES` | `429,402,529,400` | HTTP codes that trigger fallback |
| `TROOPER_PORT` | `3000` | Port trooper listens on |
| `AUTO_RECOVERY` | `false` | Set `true` to auto-recover to primary when it comes back |

---

## Recommended local models

| Model | Size | Quality | Pull command |
|---|---|---|---|
| `qwen2.5:3b` | 1.9GB | Fast, lightweight | `ollama pull qwen2.5:3b` |
| `llama3.1:8b` | 4.7GB | Best all-rounder | `ollama pull llama3.1:8b` |
| `mistral:7b` | 4.1GB | Strong reasoning | `ollama pull mistral:7b` |
| `gemma2:9b` | 5.5GB | Google's best mid-size | `ollama pull gemma2:9b` |

---

## Features

- ✅ Local first — first fallback always hits Ollama, your data stays private
- ✅ Cloud on demand — add Gemini or OpenAI keys to extend the chain
- ✅ Smart Chain — zero YAML, zero config, just set your keys
- ✅ Context preservation — full conversation history survives every provider switch
- ✅ Smart fallback — 429 retry with backoff, 402 immediate, credit balance detection
- ✅ Health headers — X-Trooper-Provider, X-Trooper-Fallback-Count, X-Trooper-Trigger
- ✅ Request timeout — 30s per provider, no hanging requests
- ✅ Unique session IDs — no context bleed between users
- ✅ Streaming support — responses re-emitted as SSE
- ✅ Zero code changes — just redirect your base URL
- ✅ Single Go binary — tiny Docker image (~10MB)
- ✅ Auto recovery — experimental, set AUTO_RECOVERY=true to enable

---

## Roadmap

**V1 — Released**
- ✅ Zero-config Claude → Ollama fallback
- ✅ Streaming support
- ✅ Configurable trigger codes

**V2 — Released**
- ✅ Smart fallback — 429 retry, 402 immediate, credit balance detection
- ✅ Context preservation — full conversation history across provider switch
- ✅ Smart Chain — local first, cloud on demand
- ✅ Health Headers — X-Trooper-Provider, X-Trooper-Fallback-Count, X-Trooper-Trigger
- ✅ Request timeout — 30s per provider
- ✅ Unique session IDs — no context bleed
- ✅ Auto Recovery — experimental feature flag (AUTO_RECOVERY=true)

**V3 — Planned**
- ⬜ Priority-aware cloud routing — let users define when to escalate from local Ollama to cloud (e.g. for complex tasks or long context) rather than always treating cloud as primary
- ⬜ Prometheus metrics endpoint
- ⬜ Grafana dashboard — fallback frequency, recovery time, cost savings
- ⬜ Local-first observability — metrics never leave your machine

---

## Recognition

- Featured in [Agent Brief](https://news.agentcommunity.org/issues/2026-04-22-the-agentic-stack) by agentcommunity.org — curated alongside Anthropic, Shopify MCP, and LangGraph updates (April 2026)
- Featured on [@github_unpacked](https://www.instagram.com/reel/DXfDrCOCNHE/) — Instagram reel with 76 saves

---

## License

MIT
