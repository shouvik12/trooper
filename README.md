# 🪖 trooper

> **A drop-in proxy that keeps your AI conversations alive.**  
> When your cloud LLM quota runs out, trooper silently falls back to a local Ollama model — full conversation context intact. Your app notices nothing.

Works with **Claude, OpenAI, Groq, Mistral, Together** — any provider, any app.

---

## How it works

```
Your App  →  http://localhost:3000  →  Claude / GPT / Groq ✅
                                    →  quota hit ⚡
                                    →  Ollama (local) 🪖 seamless fallback
```

Trooper is a **zero-code-change drop-in** — just point your base URL at trooper. One environment variable swap and you're protected.

---

## Demo

Start trooper and watch the fallback happen in real time:

```
2026/04/21 08:24:10 🪖  Trooper proxy starting on http://localhost:3000
2026/04/21 08:24:10     Primary  : https://api.anthropic.com/v1/messages
2026/04/21 08:24:10     Fallback : http://localhost:11434/api/chat (qwen2.5:3b)
2026/04/21 08:24:48 📥 POST /v1/messages (stream=false)
2026/04/21 08:24:50 ⚠️  Primary 400 — falling back to local model
2026/04/21 08:24:50 🪖  Routing to local model: qwen2.5:3b
```

Full conversation context preserved — Ollama picks up exactly where Claude left off:

```json
{
  "content": [{
    "text": "You just told me that your favorite food is pizza.",
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
# edit .env — set PRIMARY_API_KEY and choose your provider

docker compose up
```

### Local

```bash
# Prerequisites: Go 1.22+, Ollama running locally
ollama pull qwen2.5:3b
ollama serve

export PRIMARY_API_KEY=sk-ant-...
go run main.go
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
  -H "x-api-key: $PRIMARY_API_KEY" \
  -d '{
    "model": "claude-sonnet-4-20250514",
    "max_tokens": 256,
    "messages": [{"role": "user", "content": "Hello!"}]
  }'
```

---

## Configuration

| Variable | Default | Description |
|---|---|---|
| `PRIMARY_URL` | `https://api.anthropic.com/v1/messages` | Your LLM provider endpoint |
| `PRIMARY_API_KEY` | *(required)* | API key for primary provider |
| `PRIMARY_AUTH_HEADER` | `x-api-key` | `x-api-key` for Claude, `Authorization` for OpenAI/Groq/others |
| `FALLBACK_URL` | `http://localhost:11434/api/chat` | Local Ollama endpoint |
| `FALLBACK_MODEL` | `qwen2.5:3b` | Local model to fall back to |
| `QUOTA_STATUS_CODES` | `429,402,529` | HTTP codes that trigger fallback |
| `TROOPER_PORT` | `3000` | Port trooper listens on |

### Provider examples

**Claude (default):**
```env
PRIMARY_URL=https://api.anthropic.com/v1/messages
PRIMARY_API_KEY=sk-ant-...
PRIMARY_AUTH_HEADER=x-api-key
```

**OpenAI:**
```env
PRIMARY_URL=https://api.openai.com/v1/chat/completions
PRIMARY_API_KEY=sk-...
PRIMARY_AUTH_HEADER=Authorization
```

**Groq:**
```env
PRIMARY_URL=https://api.groq.com/openai/v1/chat/completions
PRIMARY_API_KEY=gsk_...
PRIMARY_AUTH_HEADER=Authorization
```

**Mistral:**
```env
PRIMARY_URL=https://api.mistral.ai/v1/chat/completions
PRIMARY_API_KEY=...
PRIMARY_AUTH_HEADER=Authorization
```

---

## Fallback behavior

| Primary status | Trooper action |
|---|---|
| `200 OK` | Pass through response |
| `429 Too Many Requests` | Fall back to local model |
| `402 Payment Required` | Fall back to local model |
| `529 Overloaded` | Fall back to local model |
| `401 Unauthorized` | Return error — bad key is not masked |
| Network error | Fall back to local model |
| Any other error | Pass through as-is |

Fallback responses include `X-Trooper-Fallback: <model>` header so you can detect them if needed.

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

- ✅ Works with any LLM provider — Claude, GPT, Groq, Mistral, Together
- ✅ Zero code changes in your app — just redirect the base URL
- ✅ Full conversation context preserved across the switch
- ✅ Streaming support — Ollama responses re-emitted as SSE
- ✅ Configurable fallback trigger codes
- ✅ Single Go binary — tiny Docker image (~10MB)
- ✅ 401 errors surface properly — bad keys aren't silently masked

---

## Roadmap

- [ ] Hand back to primary when quota resets
- [ ] Metrics endpoint — see fallback frequency
- [ ] Multiple fallback models with priority order
- [ ] Web UI for live routing visibility
- [ ] LM Studio support

---

## License

MIT
