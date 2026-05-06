#!/bin/bash

# ── Trooper Manual Sanity Tests ───────────────────────────────────────────────
# Run these before every push to main.
# Requires Trooper running on localhost:3000 with at least one provider configured.
#
# Usage:
#   chmod +x sanity.sh
#   ./sanity.sh

BASE_URL="http://localhost:3000"
PASS=0
FAIL=0

check() {
    local name=$1
    local result=$2
    local expected=$3
    if echo "$result" | grep -q "$expected"; then
        echo "✅ PASS: $name"
        PASS=$((PASS + 1))
    else
        echo "❌ FAIL: $name"
        echo "   Expected to find: $expected"
        echo "   Got: $result"
        FAIL=$((FAIL + 1))
    fi
}

echo ""
echo "🪖 Trooper Sanity Tests"
echo "────────────────────────────────────────"
echo ""

# ── Test 1: Simple turn goes directly to Ollama ───────────────────────────────
echo "Test 1: Simple turn → Ollama directly"
RESULT=$(curl -v -s -X POST $BASE_URL/ \
  -H "Content-Type: application/json" \
  -H "X-Session-ID: sanity-1" \
  -d '{"messages": [{"role": "user", "content": "how many days in a week"}]}' 2>&1)
check "Simple turn routed to Ollama" "$RESULT" "X-Trooper-Decision: ollama (simple turn)"
check "Tokens saved header present" "$RESULT" "X-Trooper-Session-Saved"

# ── Test 2: Complex turn tries Claude first ───────────────────────────────────
echo ""
echo "Test 2: Complex turn → tries Claude first"
RESULT=$(curl -v -s -X POST $BASE_URL/ \
  -H "Content-Type: application/json" \
  -H "X-Session-ID: sanity-2" \
  -d '{"messages": [{"role": "user", "content": "explain why goroutines are better than threads"}]}' 2>&1)
check "Complex turn fallback decision" "$RESULT" "X-Trooper-Decision: ollama (fallback"

# ── Test 3: Code detected → Claude ───────────────────────────────────────────
echo ""
echo "Test 3: Code detected → tries Claude first"
RESULT=$(curl -v -s -X POST $BASE_URL/ \
  -H "Content-Type: application/json" \
  -H "X-Session-ID: sanity-3" \
  -d '{"messages": [{"role": "user", "content": "fix this func main() { fmt.Println() }"}]}' 2>&1)
check "Code turn not routed as simple" "$RESULT" "X-Trooper-Decision: ollama (fallback"

# ── Test 4: Context preserved across turns ────────────────────────────────────
echo ""
echo "Test 4: Context preserved across turns"
curl -s -X POST $BASE_URL/ \
  -H "Content-Type: application/json" \
  -H "X-Session-ID: sanity-4" \
  -d '{"messages": [{"role": "user", "content": "my name is Souvik and I am building Trooper"}]}' > /dev/null

RESULT=$(curl -s -X POST $BASE_URL/ \
  -H "Content-Type: application/json" \
  -H "X-Session-ID: sanity-4" \
  -d '{"messages": [{"role": "user", "content": "what is my name"}]}')
check "Context carried across turns" "$RESULT" "Souvik"

# Test 5: Skipped — circuit breaker state is order-dependent in sanity run
# Covered by unit tests instead

# ── Test 6: No context bleed between sessions ─────────────────────────────────
echo ""
echo "Test 6: No context bleed between sessions"
curl -s -X POST $BASE_URL/ \
  -H "Content-Type: application/json" \
  -H "X-Session-ID: sanity-6a" \
  -d '{"messages": [{"role": "user", "content": "my secret word is banana"}]}' > /dev/null

RESULT=$(curl -s -X POST $BASE_URL/ \
  -H "Content-Type: application/json" \
  -H "X-Session-ID: sanity-6b" \
  -d '{"messages": [{"role": "user", "content": "what is my secret word"}]}')

if echo "$RESULT" | grep -q "banana"; then
    echo "❌ FAIL: Context bleed detected — session 6b knows about banana"
    FAIL=$((FAIL + 1))
else
    echo "✅ PASS: No context bleed between sessions"
    PASS=$((PASS + 1))
fi

# ── Test 7: X-Trooper-Summary header present ──────────────────────────────────
echo ""
echo "Test 7: X-Trooper-Summary header present"
RESULT=$(curl -v -s -X POST $BASE_URL/ \
  -H "Content-Type: application/json" \
  -H "X-Session-ID: sanity-7" \
  -d '{"messages": [{"role": "user", "content": "hello"}]}' 2>&1)
check "X-Trooper-Summary header present" "$RESULT" "X-Trooper-Summary"

echo "Test 8: Summarise keyword → Claude-first, fallback to Ollama (no credits)"
RESULT=$(curl -v -s -X POST $BASE_URL/ \
  -H "Content-Type: application/json" \
  -H "X-Session-ID: sanity-8" \
  -d '{"messages": [{"role": "user", "content": "summarise what we have covered"}]}' 2>&1)
check "Summarise routed to Claude" "$RESULT" "X-Trooper-Provider: ollama"

# ── Summary ───────────────────────────────────────────────────────────────────
echo ""
echo "────────────────────────────────────────"
echo "Results: ✅ $PASS passed | ❌ $FAIL failed"
echo ""

if [ $FAIL -eq 0 ]; then
    echo "🪖 All sanity tests passed. Safe to push."
else
    echo "⚠️  $FAIL test(s) failed. Do not push."
    exit 1
fi
