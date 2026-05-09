# 🪖 Trooper

> **Your LLM didn't crash — it fell back and kept going.**

Quota errors should be invisible.

As LLM APIs get rate-limited and expensive, local fallback isn't optional anymore.

```
→ Claude fails       → continues on Ollama
→ Simple prompts     → never hit the cloud
→ Every response     → shows tokens saved
```

**Trooper is a circuit breaker + router + context engine for LLMs.**

<img width="2070" height="582" alt="image" src="https://github.com/user-attachments/assets/e44f1843-5a6b-4f52-bd3e-37fffadb0b85" />

---

## What you see

Every response tells you exactly what happened — no dashboards, no setup:

```bash
# Simple question → Ollama handled it, cloud never contacted
X-Trooper-Provider: ollama
X-Trooper-Decision: ollama (simple turn) | cloud skipped
X-Trooper-Session-Saved: 42 tokens

# Complex question → Claude handled it
X-Trooper-Provider: claude
X-Trooper-Summary: claude (direct) ✓

# Claude quota hit → fell back to Ollama, context preserved
X-Trooper-Provider: ollama
X-Trooper-Decision: ollama (fallback: credit_balance)
X-Trooper-Session-Saved: 42 tokens
X-Trooper-Summary: claude → ollama (credit_balance) | context ✓
```

`X-Trooper-Session-Saved` accumulates across the session — every turn routed locally instead of to a paid API adds to the count.

---

## What Trooper is

Trooper is a drop-in proxy for LLM apps. When cloud models fail — quota, rate limits, outages — it automatically falls back to your local Ollama instance while preserving full conversation context.

No retries. No crashes. No lost sessions. ⏱ Runs in under 60 seconds.

---

## Why not LiteLLM or Bifrost

LiteLLM and Bifrost route between cloud providers.

Trooper is built for a different failure mode: when the cloud stops working.

| | LiteLLM / Bifrost | Trooper |
|---|---|---|
| Fallback target | Another cloud provider | Your local machine |
| Setup | `pip install`, venv, YAML | One Go binary, env vars |
| Dependencies | Heavy Python stack | Zero — pure stdlib |
| Works offline | ❌ | ✅ |
| Data on fallback | Goes to another cloud | Stays on your machine |

When LiteLLM falls back, your data goes to another cloud. When Trooper falls back, your data goes to your machine.

---

## Smart routing

Trooper decides when the cloud is overkill.

> **The classifier is rule-based and deterministic — no LLM call, no latency, no cost to classify.** Most routing tools call an LLM to decide routing. Trooper doesn't.

Simple, stateless requests route directly to your local Ollama — no API call, no cost:

```
"how many days in a week"  →  Ollama directly 🪖  (cloud never contacted)
"explain why goroutines…"  →  Claude ✅           (needs reasoning)
```

**Routes to Ollama:** factual lookups, definitions, formatting, conversation meta, short stateless summaries

**Always goes to Claude:** reasoning, judgment, multi-step tasks, context-aware summaries, code, messages over 20 words

---

## How Trooper handles context

The hard part of fallback isn't switching models — it's keeping context.

Trooper solves that with a 3-layer compaction system:

```
ANCHOR  (~10%)  — First 2 turns verbatim, never dropped
SITREP  (~20%)  — Rule-based summary of middle turns
TAIL    (~70%)  — Last N turns verbatim
                  Total <= 6144 tokens (configurable)
```

The SITREP is extracted automatically — no LLM call needed. From a real session:

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
    Anchor turns   : 2 (~180 tokens)
    Middle turns   : 2 → SITREP (~148 tokens)
    Recent turns   : 1 (~36 tokens)
    Tokens used    : 364 / 6144
```

> **Honest note:** Compaction is lossy by design. The SITREP preserves intent and state — not verbatim history. For precision-critical workflows, keep sessions short or increase `CONTEXT_WINDOW`.

---

## Quickstart

⏱ Runs in under 60 seconds.

### Prerequisites

```bash
ollama pull qwen2.5:3b
```

> 💡 **Eliminate cold-start latency** — set `OLLAMA_KEEP_ALIVE=24h` in your Ollama systemd service. Without this, the first fallback after idle takes 3–5s for 7B models, up to 20s for 72B. Add to your systemd service:
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
go run main.go providers.go classifier.go
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
  -d '{"model": "claude-3-5-haiku-20241022", "messages": [{"role": "user", "content": "Hello!"}]}'
```

Pass `X-Session-ID` to track named sessions. Without it, Trooper assigns a unique auto session per request.

---

## Provider chain

Trooper builds the chain from environment variables. Ollama is always last.

```bash
CLAUDE_API_KEY=sk-ant-...                          # Chain: Claude → Ollama
CLAUDE_API_KEY=sk-ant-...  GEMINI_API_KEY=AIza...  # Chain: Claude → Gemini → Ollama
CLAUDE_API_KEY=sk-ant-...  OPENAI_API_KEY=sk-...   # Chain: Claude → OpenAI → Ollama
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

```bash
curl http://localhost:3000/ ... -v 2>&1 | grep X-Trooper

# Simple turn — cloud never contacted
X-Trooper-Provider: ollama
X-Trooper-Decision: ollama (simple turn) | cloud skipped
X-Trooper-Session-Saved: 14 tokens

# Cloud served normally
X-Trooper-Provider: claude
X-Trooper-Fallback-Count: 0
X-Trooper-Summary: claude (direct) ✓

# Quota hit — fell back, context preserved
X-Trooper-Provider: ollama
X-Trooper-Fallback-Count: 1
X-Trooper-Decision: ollama (fallback: credit_balance)
X-Trooper-Session-Saved: 14 tokens
X-Trooper-Summary: claude → ollama (credit_balance) | context ✓
```

---

## Circuit breaker

If a provider fails 3 times within 60 seconds, Trooper skips it automatically — no wasted round trips. Resets after 60 seconds.

```
⚡ Skipping claude — circuit open (3 fails in last 60s)
🔄 Trying provider: ollama
```

---

## Auto recovery

```bash
AUTO_RECOVERY=true go run main.go providers.go classifier.go
```

Health checks use a free `GET /models` endpoint — no inference requests, no cost. Trooper silently routes back to the primary provider when it recovers.

---

## Running tests

```bash
go test ./... -v
```

Covers: turn classifier, code detection, context compaction, token estimation. All tests must pass before any contribution is merged.

---

## Configuration

| Variable | Default | Description |
|---|---|---|
| `CLAUDE_API_KEY` | — | Anthropic API key |
| `CLAUDE_MODEL` | — | Default Claude model |
| `GEMINI_API_KEY` | — | Google Gemini API key |
| `GEMINI_MODEL` | `gemini-2.0-flash` | Default Gemini model |
| `OPENAI_API_KEY` | — | OpenAI API key |
| `OPENAI_MODEL` | `gpt-4o-mini` | Default OpenAI model |
| `OLLAMA_MODEL` | `qwen2.5:3b` | Local fallback model |
| `FALLBACK_URL` | `http://localhost:11434/api/chat` | Ollama endpoint |
| `CONTEXT_WINDOW` | `6144` | Token budget for context compaction |
| `QUOTA_STATUS_CODES` | `429,402,529,400` | HTTP codes that trigger fallback |
| `TROOPER_PORT` | `3000` | Port Trooper listens on |
| `TROOPER_BIND` | `127.0.0.1` | Bind address |
| `AUTO_RECOVERY` | `false` | Enable automatic recovery to primary provider |
| `OLLAMA_KEEP_ALIVE` | `5m` | Set `24h` in systemd to eliminate cold-start latency |

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

**V3.1 — Released**
- ✅ Smart routing — simple turns route to Ollama directly, cloud never contacted
- ✅ X-Trooper-Session-Saved header — cumulative tokens saved per session
- ✅ X-Trooper-Decision header — routing decision on every response
- ✅ Deterministic classifier — no LLM call to route, zero added latency

**V3.0 — Released**
- ✅ Circuit breaker — skip providers that fail 3x in 60s
- ✅ Zero-interruption log lines
- ✅ X-Trooper-Summary header

**V2 / V2.2 — Released**
- ✅ Cloud → Ollama fallback with session continuity
- ✅ Context compaction — Anchor + SITREP + Tail
- ✅ Streaming, health check, auto recovery, zero dependencies

---

## Recognition

- Featured in [Agent Brief](https://news.agentcommunity.org/issues/2026-04-22-the-agentic-stack) by agentcommunity.org — curated alongside Anthropic, Shopify MCP, and LangGraph updates (April 2026)
- Featured on [@github_unpacked](https://www.instagram.com/reel/DXfDrCOCNHE/) — Instagram reel with 76 saves
- Featured on [PatentLLM](https://media.patentllm.org/news/local-ai/qwen3-6-27b-local-inference-on-rtx-3090-with-native-vllm-oll-20260502) — covered alongside Qwen3.6-27B RTX 3090 local inference story (May 2026)
- Featured on [dev.to](https://dev.to/soytuber/qwen36-27b-local-inference-on-rtx-3090-with-native-vllm-ollama-fallback-2jgg) — local AI tooling roundup (May 2026)
- Cited by [kylebrodeur](https://github.com/kylebrodeur) as inspiration for *"robust, transparent HTTP rate-limit fallback triggers"*

---

## License

MIT
