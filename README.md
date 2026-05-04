# 🪖 Trooper

> **Zero-interruption AI infrastructure.**  
> Cloud fails. Trooper doesn't.

<img width="2070" height="582" alt="image" src="https://github.com/user-attachments/assets/e44f1843-5a6b-4f52-bd3e-37fffadb0b85" />


---

## What Trooper is

Trooper is a lightweight proxy that sits between your app and your cloud LLM providers. When Claude, Gemini, or OpenAI hits a quota limit or fails, Trooper automatically falls back to a local Ollama instance and carries the session context with it.

Your app never changes. The conversation continues.

```
Your App → Trooper → Claude  ✅  (works normally)
                   → quota hit ⚡
                   → Ollama (local) 🪖  (session continues)
```

---

## Why not LiteLLM or Bifrost

LiteLLM and Bifrost are excellent tools — but they solve a different problem.

**LiteLLM** is a Python gateway for teams routing between 100+ cloud providers. Setup requires a Python environment, venv, and config files. Its fallback is always another cloud provider — your data still leaves your machine.

**Bifrost** is enterprise-scale infrastructure for organisations managing multiple cloud accounts and compliance requirements.

**Trooper is for individual developers and small teams** who run local models on their own hardware and want cloud quota exhaustion to just be handled — without setting up a full gateway stack.

The fundamental difference:

| | LiteLLM / Bifrost | Trooper |
|---|---|---|
| Fallback target | Another cloud provider | Your local machine |
| Setup | `pip install`, venv, YAML | One Go binary, env vars |
| Dependencies | Heavy Python stack | Zero — pure stdlib |
| Works offline | ❌ | ✅ |
| Data on fallback | Goes to another cloud | Stays on your machine |

When LiteLLM falls back, your data goes to another cloud. When Trooper falls back, your data goes to your machine.

---

## How Trooper handles context

A naive proxy would route the next request to Ollama cold — with no knowledge of what was discussed. The conversation breaks.

Trooper maintains server-side session history and uses a three-layer context compaction system to carry the session forward when the budget is exceeded:

```
ANCHOR  (~10%)  — First 2 turns verbatim, never dropped
SITREP  (~20%)  — Rule-based summary of middle turns
TAIL    (~70%)  — Last N turns verbatim
                  Total <= 6144 tokens (configurable)
```

The SITREP is extracted automatically from the middle turns using rule-based signal classification — no LLM call needed. It looks like this in practice (from a real test session):

```
SITREP: intent="building a go proxy called trooper that falls back to local"
        stage=in_progress confidence=1.00
        open=5 actions=5 resolved=1
```

The full JSON sent to Ollama:

```json
[TROOPER_SITREP]{
  "intent": "building a go proxy called trooper that falls back to local",
  "stage": "in_progress",
  "constraints": ["local-first", "proxy-layer"],
  "active_entities": ["Trooper", "Ollama", "Claude"],
  "open_loops": ["streaming pending"],
  "recent_actions": ["deploy monday", "check streaming"],
  "resolved_loops": ["resolve the health check"],
  "confidence": 1.00
}[/TROOPER_SITREP]
```

Compaction triggers automatically when the session exceeds the token budget:

```
📦  Context compaction triggered — 1532 tokens exceeds 6144 budget
    Total turns    : 5
    Anchor turns   : 2 (~180 tokens)
    Middle turns   : 2 → SITREP (~148 tokens)
    Recent turns   : 1 (~36 tokens)
    Tokens used    : 364 / 6144
    SITREP         : intent="building a go proxy" stage=in_progress
                     confidence=1.00 open=5 actions=5 resolved=1
```

**What this actually means:** Ollama receives a compressed picture of the conversation — topic, intent, what's resolved, what's pending, and recent actions. It responds in context rather than starting cold. Quality of continuation depends on your local model — a 3B model will follow the topic but may not match the precision of a larger model.

> **Honest note:** Compaction is lossy by design. The SITREP preserves intent and state — not verbatim history. Signal extraction is rule-based, not LLM-generated, so phrase quality varies. For most conversational use cases this is sufficient. For precision-critical workflows, keep sessions short or increase `CONTEXT_WINDOW`.

---

## Quickstart

### Prerequisites
- Ollama running locally with at least one model pulled

```bash
ollama pull qwen2.5:3b
```

> 💡 **Eliminate cold-start latency on fallback** — set `OLLAMA_KEEP_ALIVE=24h` in your Ollama systemd service to keep the model loaded all day. Without this, the first fallback request after idle takes 3–5s for 7B models and up to 20s for 72B models. Add this to your systemd service file:
> ```
> Environment="OLLAMA_KEEP_ALIVE=24h"
> ```

### Option 1 — Docker (no Go required)

```bash
git clone https://github.com/shouvik12/trooper
cd trooper

cp .env.example .env
# edit .env — set CLAUDE_API_KEY

docker compose up
```

### Option 2 — Run from source (Go 1.22+)

```bash
git clone https://github.com/shouvik12/trooper
cd trooper

export CLAUDE_API_KEY=sk-ant-...
go run main.go providers.go
```

Trooper starts on `http://127.0.0.1:3000`. Binds to localhost by default — your API keys are not exposed on the network.

---

## Usage

Point your existing client at Trooper — nothing else changes:

**Python + Anthropic SDK:**
```python
import anthropic

client = anthropic.Anthropic(
    api_key="your-key",
    base_url="http://localhost:3000",  # only change
)
```

**Python + OpenAI SDK:**
```python
from openai import OpenAI

client = OpenAI(
    api_key="your-key",
    base_url="http://localhost:3000",  # only change
)
```

**curl:**
```bash
curl http://localhost:3000/ \
  -H "Content-Type: application/json" \
  -H "X-Session-ID: my-session" \
  -d '{
    "model": "claude-3-5-haiku-20241022",
    "messages": [{"role": "user", "content": "Hello!"}]
  }'
```

Pass `X-Session-ID` to track named sessions. Without it, Trooper assigns a unique auto session per request — no context bleed between users.

---

## Provider chain

Trooper builds the chain from environment variables. Ollama is always last.

```bash
# Claude only (default)
CLAUDE_API_KEY=sk-ant-...
# Chain: Claude → Ollama

# Claude + Gemini
CLAUDE_API_KEY=sk-ant-...
GEMINI_API_KEY=AIza...
# Chain: Claude → Gemini → Ollama

# Full chain
CLAUDE_API_KEY=sk-ant-...
GEMINI_API_KEY=AIza...
OPENAI_API_KEY=sk-...
# Chain: Claude → Gemini → OpenAI → Ollama
```

---

## Fallback behaviour

| Status | Trooper action |
|---|---|
| `200 OK` | Pass through |
| `429 Rate Limited` | Retry with 2s backoff, then try next |
| `402 Payment Required` | Fall back immediately |
| `400 Credit Balance` | Detect credit error, fall back immediately |
| `401 Unauthorized` | Surface error — bad keys are never masked |
| `529 Overloaded` | Fall back immediately |
| Network error | Fall back immediately — 30s timeout per provider |

---

## Response headers

Every response tells you exactly what happened:

```bash
curl http://localhost:3000/ ... -v 2>&1 | grep X-Trooper

# Cloud served normally
X-Trooper-Provider: claude
X-Trooper-Fallback-Count: 0
X-Trooper-Summary: claude (direct) ✓

# Quota hit, fell back to Ollama
X-Trooper-Provider: ollama
X-Trooper-Fallback-Count: 1
X-Trooper-Summary: claude → ollama (credit_balance) | context ✓
```

---

## Circuit breaker

Trooper tracks provider failures. If a provider fails 3 times within 60 seconds, Trooper skips it automatically and routes directly to the next provider — no wasted round trips.

```
⚡ Skipping claude — circuit open (3 fails in last 60s)
🔄 Trying provider: ollama
🪖 Fallback: claude → ollama () | request preserved
```

The circuit resets automatically after 60 seconds.

---

## Auto recovery

When a cloud provider comes back online, Trooper can silently route back to it. Disabled by default.

```bash
AUTO_RECOVERY=true go run main.go providers.go
```

Health checks use a free `GET /models` endpoint — no inference requests, no cost.

```
🏥 Auto recovery enabled — checking every 60 seconds
🔄 Auto recovery — switching back to claude
```

---

## Configuration

| Variable | Default | Description |
|---|---|---|
| `CLAUDE_API_KEY` | — | Anthropic API key |
| `CLAUDE_MODEL` | — | Default Claude model (request model takes priority if set) |
| `GEMINI_API_KEY` | — | Google Gemini API key |
| `GEMINI_MODEL` | `gemini-2.0-flash` | Default Gemini model |
| `OPENAI_API_KEY` | — | OpenAI API key |
| `OPENAI_MODEL` | `gpt-4o-mini` | Default OpenAI model |
| `OLLAMA_MODEL` | `qwen2.5:3b` | Local fallback model |
| `FALLBACK_URL` | `http://localhost:11434/api/chat` | Ollama endpoint |
| `CONTEXT_WINDOW` | `6144` | Token budget for context compaction |
| `QUOTA_STATUS_CODES` | `429,402,529,400` | HTTP codes that trigger fallback |
| `TROOPER_PORT` | `3000` | Port Trooper listens on |
| `TROOPER_BIND` | `127.0.0.1` | Bind address — set `0.0.0.0` only if you need network access |
| `AUTO_RECOVERY` | `false` | Enable automatic recovery to primary provider |
| `OLLAMA_KEEP_ALIVE` | `5m` (Ollama default) | Set `24h` in your Ollama systemd service to keep model loaded — eliminates cold-start latency on fallback |

---

## Recommended local models

| Model | Size | Notes |
|---|---|---|
| `qwen2.5:3b` | 1.9GB | Default — fast, lightweight |
| `qwen2.5:7b` | 4.7GB | Better quality, still fast |
| `llama3.1:8b` | 4.9GB | Strong all-rounder |
| `mistral:7b` | 4.1GB | Good reasoning |

---

## Roadmap

**V2 — Released**
- ✅ Cloud → Ollama fallback with session continuity
- ✅ Context compaction — Anchor + SITREP + Tail
- ✅ Rule-based SITREP extraction — intent, stage, open loops, actions, resolved
- ✅ Live streaming — tokens pipe through in real time
- ✅ Health check — free `GET /models`, no inference cost
- ✅ Smart fallback — 429 retry, 402 immediate, credit balance detection
- ✅ Response headers — provider, fallback count, trigger
- ✅ Session TTL — 24hr expiry, 10min cleanup sweep
- ✅ Secure by default — binds to `127.0.0.1`
- ✅ Zero dependencies — pure Go stdlib
- ✅ Auto recovery — experimental (`AUTO_RECOVERY=true`)

**V2.2 — Released**
- ✅ Per-request provider selection — removes global race condition
- ✅ Context cancellation — upstream calls respect client disconnect
- ✅ Session store architecture — moved to main(), explicit dependency

**V3.0 — Released**
- ✅ Circuit breaker — skip providers that fail 3x in 60s
- ✅ Zero-interruption log lines — `🪖 Fallback/Provider` visibility
- ✅ X-Trooper-Summary header — observability out of the box

**V3.1 — Planned**
- ⬜ Context checksums — verify context integrity across fallback
- ⬜ SITREP v2 — improved intent extraction on longer conversations
- ⬜ Provider abstraction — per-provider adapters, no more if-chains
- ⬜ Prometheus metrics endpoint

---

## Recognition

- Featured in [Agent Brief](https://news.agentcommunity.org/issues/2026-04-22-the-agentic-stack) by agentcommunity.org — curated alongside Anthropic, Shopify MCP, and LangGraph updates (April 2026)
- Featured on [@github_unpacked](https://www.instagram.com/reel/DXfDrCOCNHE/) — Instagram reel with 76 saves

---

## License

MIT
