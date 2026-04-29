# 🪖 trooper

> **Local by default. Cloud when you choose.**  
> Trooper is a zero-config proxy that keeps your AI conversations alive. When your cloud LLM fails, it falls back to local Ollama — full conversation context intact. Your app never knows.

---

## Why Trooper

Most LLM proxies route cloud-to-cloud. Trooper is different — your first fallback is always local. Private. No data leaves your machine unless you choose it.

```
Default:     Claude → Ollama (private, always)
Opt-in:      Claude → Gemini → OpenAI → Ollama (cloud when you choose)
```

Zero config. Zero YAML. Just set your API keys.

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

```
2026/04/29 08:45:58 🪖  Trooper proxy starting on http://localhost:3000
2026/04/29 08:45:58     Provider 1: claude
2026/04/29 08:45:58     Provider 2: ollama
2026/04/29 08:55:23 📥 POST /v1/messages (stream=false, session=abc-123)
2026/04/29 08:55:24 🔄 Trying provider: claude
2026/04/29 08:55:24 ⚠️  claude 400 — credit balance too low, trying next
2026/04/29 08:55:24 🔄 Trying provider: ollama
2026/04/29 08:55:24 🪖  Routing to local model: qwen2.5:3b
```

Full conversation context preserved — Ollama picks up exactly where Claude left off:

```json
{
  "content": [{
    "text": "You mentioned earlier that your project deadline is Friday and you prefer TypeScript over JavaScript.",
    "type": "text"
  }],
  "model": "qwen2.5:3b",
  "id": "trooper-fallback"
}
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
# Default — local by default
CLAUDE_API_KEY=sk-ant-...
# Chain: Claude → Ollama

# Opt-in cloud fallback
CLAUDE_API_KEY=sk-ant-...
GEMINI_API_KEY=AIza...
# Chain: Claude → Gemini → Ollama

# Full cloud chain
CLAUDE_API_KEY=sk-ant-...
GEMINI_API_KEY=AIza...
OPENAI_API_KEY=sk-...
# Chain: Claude → Gemini → OpenAI → Ollama
```

Ollama is always last. Always private. Always there.

---

## Context Preservation

Trooper maintains full conversation history server-side per session. When your cloud provider fails mid-conversation, Ollama picks up with complete context intact.

Pass `X-Session-ID` header to enable session tracking:

```bash
curl http://localhost:3000/v1/messages \
  -H "X-Session-ID: my-session" \
  ...
```

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
| Network error | Fall back immediately |

---

## Health Headers

Every response includes Trooper's routing decision:

```
X-Trooper-Provider: ollama
X-Trooper-Fallback-Count: 1
X-Trooper-Trigger: credit_balance
```

Log them, alert on them, build dashboards around them.

---

## Auto Recovery *(experimental)*

Trooper can automatically recover to the best available provider when it comes back online. Disabled by default — enable with a feature flag:

```bash
AUTO_RECOVERY=true go run main.go providers.go
```

When enabled, Trooper runs a background health check every 60 seconds. If a higher-priority provider recovers, Trooper silently routes back to it. Your app always gets the best available model without any intervention.

> ⚠️ This feature is experimental. Enable it when you're comfortable with the behaviour in your environment.

---

## Configuration

| Variable | Default | Description |
|---|---|---|
| `CLAUDE_API_KEY` | *(required)* | Anthropic API key |
| `GEMINI_API_KEY` | *(optional)* | Google Gemini API key — enables cloud fallback |
| `OPENAI_API_KEY` | *(optional)* | OpenAI API key — enables cloud fallback |
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

- ✅ Local by default — first fallback always hits Ollama, never a third party
- ✅ Smart Chain — Claude → Gemini → OpenAI → Ollama, opt-in cloud fallback
- ✅ Context preservation — full conversation history survives every provider switch
- ✅ Smart fallback — 429 retry, 402 immediate, credit balance detection
- ✅ Health headers — X-Trooper-Provider, X-Trooper-Fallback-Count, X-Trooper-Trigger
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
- ✅ Smart Chain — local by default, cloud when you choose
- ✅ Health Headers — X-Trooper-Provider, X-Trooper-Fallback-Count, X-Trooper-Trigger
- ✅ Auto Recovery — experimental feature flag (AUTO_RECOVERY=true)

**V3 — Planned**
- ⬜ Priority-aware cloud routing — let users define when to escalate from local Ollama to cloud (e.g. for complex tasks or long context) rather than always treating cloud as the primary
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
