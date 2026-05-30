**NEW:** [Live dashboard demo →](http://127.0.0.1:3000/dashboard) | [Subagent recovery demo →](https://youtu.be/NN2uwQZDCck) | [Subagent recovery article →](https://dev.to/shouvik12/i-added-a-recovery-endpoint-to-my-llm-proxy-so-agents-never-lose-progress-mid-task-524b) | [4-agent privacy routing demo →](https://dev.to/shouvik12/i-tested-privacy-aware-routing-with-4-ai-agents-what-actually-stayed-local-39oa)

# 🪖 Trooper

> **Your agent communicates over HTTP to an LLM. Trooper can observe it.**

Trooper started as a fallback proxy. It's now an active observer.

Your agent runs. Trooper watches. You see everything — intent, open loops, completed steps, full transcript. Live. Zero instrumentation.

```
→ Any agent          → point at Trooper, open dashboard, see everything
→ Claude fails       → continues on Ollama, context preserved
→ Simple prompts     → never hit the cloud
→ Agent mid-task     → /recovery tells you exactly where to resume
```

**Trooper is a zero-instrumentation agent observability platform with local fallback.**

<img width="971" height="832" alt="Trooper Dashboard" src="https://github.com/user-attachments/assets/2f22f3fb-55a5-4f68-ad9a-cb1d691930a0" />


---

## What you see

**In the dashboard** — open `http://localhost:3000/dashboard` while your agent runs:

- **Intent** — what your agent is trying to do, extracted automatically
- **Open Loops** — what it's stuck on, highlighted in real time
- **Completed Steps** — what it finished, tracked as it happens
- **Session Transcript** — every message, colour coded by role

**In every response header** — no dashboards required:

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

---

## What Trooper is

Trooper is a drop-in proxy that sits between your agent and any LLM provider. It observes every request, extracts intent and signals, and builds a live picture of what your agent is doing — all without touching your code.

When cloud models fail — quota, rate limits, outages — it automatically falls back to your local Ollama instance while preserving full conversation context.

**Trooper is no longer passive.** It started as a fallback proxy. Now it watches every session actively and makes that data visible.

No retries. No crashes. No lost sessions. No SDK. No instrumentation. ⏱ Runs in under 60 seconds.

---

## Who uses Trooper

**Agent builders** — see exactly what your agent is doing, what it's stuck on, and what it completed. Zero instrumentation — just point your agent at Trooper.

**App developers** — your users never see quota errors. Trooper falls over to local Ollama transparently while your app keeps running.

**Claude Code / Cursor users** — coding sessions survive quota hits. No lost context, no starting over.

**Privacy-conscious developers** — use `x_force_local` to keep sensitive requests off the cloud without interrupting the session.

---

## Why not LiteLLM, Bifrost, or Helicone

| | LiteLLM / Bifrost | Helicone | Trooper |
|---|---|---|---|
| Observability | ❌ | Request-level only | ✅ Intent, open loops, completed steps |
| Instrumentation needed | SDK required | None | None |
| Fallback target | Another cloud | Another cloud | Your local machine |
| Local / private | ❌ | ❌ Cloud only | ✅ Data never leaves machine |
| Setup | `pip install`, YAML | API key, cloud account | One Go binary, env vars |
| Status | Active | Maintenance mode | Active |

Helicone is the closest — proxy-based, zero instrumentation. But it went into maintenance mode in March 2026 and sends your data to their cloud. Trooper is the open source, local-first alternative.

---

## Live Dashboard

Point any agent at Trooper. Open `http://localhost:3000/dashboard`. See everything.

```bash
# Start Trooper
go run .

# Point your agent at Trooper
export ANTHROPIC_BASE_URL=http://localhost:3000
export OPENAI_BASE_URL=http://localhost:3000

# Open dashboard — no session ID needed
open http://localhost:3000/dashboard
```

The dashboard shows all active sessions. Click any session to see:

- Real-time intent extraction
- Open loops highlighted in red as they appear
- Completed steps in green as they resolve
- Full session transcript

Auto-refreshes every 5 seconds. No page reload needed.

**List all active sessions:**
```bash
curl http://localhost:3000/sessions
```

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

> **Honest note:** Compaction is lossy by design. The SITREP preserves intent and state — not verbatim history. For precision-critical workflows, keep sessions short or increase `CONTEXT_WINDOW`.

---

## Quickstart

⏱ Runs in under 60 seconds.

### Option 1 — Docker (no Go required)

```bash
git clone https://github.com/shouvik12/trooper
cd trooper
cp .env.example .env
# edit .env — set CLAUDE_API_KEY
docker compose up

# First run: pull the model into the Ollama container
docker compose exec ollama ollama pull qwen2.5:3b
```

### Option 2 — Run from source (Go 1.22+)

### Prerequisites

```bash
ollama pull qwen2.5:3b
```

```bash
git clone https://github.com/shouvik12/trooper
cd trooper
export CLAUDE_API_KEY=sk-ant-...
go run .
```

Trooper starts on `http://127.0.0.1:3000`. Open `http://127.0.0.1:3000/dashboard` in your browser.

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
curl http://localhost:3000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "X-Session-ID: my-session" \
  -d '{"model": "claude-haiku-4-5", "max_tokens": 1024, "messages": [{"role": "user", "content": "Hello!"}]}'
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
| `400 Credit Balance / Invalid Key` | Fall back immediately |
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
AUTO_RECOVERY=true go run .
```

Health checks use a free `GET /models` endpoint — no inference requests, no cost. Trooper silently routes back to the primary provider when it recovers.

---

## Per-request local routing

Add `x_force_local: true` to any request body to route that specific request to Ollama:

```bash
curl http://localhost:3000/v1/chat/completions \
  -H "X-Session-ID: dev-session" \
  -d '{"model": "claude-haiku-4-5", "max_tokens": 1024,
       "x_force_local": true,
       "messages": [{"role": "user", "content": "Our payment vault uses..."}]}'
```

---

## Subagent recovery

Trooper tracks every step your agent completes in real time. When something fails mid-task:

```bash
GET http://localhost:3000/recovery/{session_id}
```

Response:

```json
{
  "session_id": "my-agent-session-1",
  "completed_steps": [
    "completed pr #1",
    "completed pr #2",
    "completed pr #3"
  ],
  "resume_from": 4,
  "recovery_hint": "Resume from step 4"
}
```

**Demo:** [Agent hits quota on PR #4 of 8 — Trooper recovers it in seconds →](https://youtu.be/NN2uwQZDCck)

---

## Running tests

```bash
go test ./... -v
./sanity.sh
```

Covers: turn classifier, code detection, context compaction, token estimation, subagent step tracking, agent observability.

---

## Configuration

| Variable | Default | Description |
|---|---|---|
| `CLAUDE_API_KEY` | — | Anthropic API key |
| `CLAUDE_MODEL` | `claude-haiku-4-5` | Default Claude model |
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

**V3.3 — Released**
- ✅ Live dashboard — `localhost:3000/dashboard` shows intent, open loops, completed steps, transcript
- ✅ Sessions endpoint — `localhost:3000/sessions` lists all active sessions
- ✅ Zero instrumentation agent observability — just a URL change

**V3.2 — Released**
- ✅ Subagent recovery — `/recovery/{session_id}` endpoint tracks completed steps in real time
- ✅ Response normalization — Claude direct responses wrapped in OpenAI-compatible format
- ✅ Broader 400 fallback — invalid keys and auth errors now trigger local fallback

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
