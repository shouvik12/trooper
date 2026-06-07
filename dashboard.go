package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ── Dashboard Handler ─────────────────────────────────────────────────────────

func dashboardHandler(store *SessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessionID := r.URL.Query().Get("session")
		if sessionID == "" {
			http.Error(w, "session parameter required", http.StatusBadRequest)
			return
		}

		messages := store.GetAll(sessionID)
		entries := store.GetEntries(sessionID)
		fallbackEvents := store.GetFallbackEvents(sessionID)
		completed := extractCompletedSteps(messages)
		sitrep := buildSITREP(messages, extractLatestUserMessage2(messages))
		tokensSaved := store.GetTokensSaved(sessionID)
		compressionRatio := store.GetCompressionRatio(sessionID)
		rawSITREP := store.GetRawSITREP(sessionID)
		startTime := store.GetStartTime(sessionID)
		duration := ""
		if !startTime.IsZero() {
			d := time.Since(startTime)
			duration = fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
		}

		// Determine current provider (last known)
		currentProvider := "unknown"
		if len(entries) > 0 {
			for i := len(entries) - 1; i >= 0; i-- {
				if entries[i].Provider != "" {
					currentProvider = entries[i].Provider
					break
				}
			}
		}

		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, dashboardHTML,
			sessionID,
			sessionID,
			sitrep.Intent,
			sitrep.Intent,
			currentProvider,
			tokensSaved,
			compressionRatio,
			fmt.Sprintf("%.0f%%", sitrep.Confidence*100),
			duration,
			len(fallbackEvents),
			renderEntities(sitrep.Entities),
			len(fallbackEvents),
			renderFallbackEvents(fallbackEvents),
			len(completed),
			renderCompleted(completed),
			len(sitrep.OpenLoops),
			renderOpenLoopsNew(sitrep.OpenLoops),
			len(sitrep.ResolvedLoops),
			renderResolvedLoops(sitrep.ResolvedLoops),
			rawSITREP,
			renderTranscript(entries),
		)
	}
}

// ── Dashboard Index Handler ───────────────────────────────────────────────────

func dashboardIndexHandler(store *SessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("session") != "" {
			dashboardHandler(store)(w, r)
			return
		}

		store.mu.RLock()
		sessions := make([]string, 0, len(store.sessions))
		for id := range store.sessions {
			sessions = append(sessions, id)
		}
		store.mu.RUnlock()

		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<title>Trooper — Sessions</title>
<style>
body{font-family:monospace;max-width:900px;margin:40px auto;padding:20px;background:#0d1117;color:#e0e0e0}
h1{color:#4ade80}
.session{border:1px solid #333;border-radius:8px;padding:16px;margin:12px 0;background:#161b22;cursor:pointer}
.session:hover{border-color:#4ade80}
.session a{color:#4ade80;text-decoration:none;font-size:16px}
.empty{color:#555;margin-top:40px}
</style>
</head>
<body>
<h1>&#x1FA96; Trooper</h1>
<p style="color:#888">Active sessions</p>
`)
		if len(sessions) == 0 {
			fmt.Fprintf(w, `<p class="empty">No active sessions. Point your agent at Trooper to get started.</p>`)
		} else {
			for _, id := range sessions {
				fmt.Fprintf(w, `<div class="session"><a href="/dashboard?session=%s">%s →</a></div>`, id, id)
			}
		}
		fmt.Fprintf(w, `<p style="color:#555;font-size:12px;margin-top:40px">Auto-refreshes every 5 seconds</p>
<script>setTimeout(()=>location.reload(),5000)</script>
</body></html>`)
	}
}

// ── Sessions Handler ──────────────────────────────────────────────────────────

func sessionsHandler(store *SessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		store.mu.RLock()
		sessions := make([]string, 0, len(store.sessions))
		for id := range store.sessions {
			sessions = append(sessions, id)
		}
		store.mu.RUnlock()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"sessions": sessions,
			"count":    len(sessions),
		})
	}
}

// ── Render Helpers ────────────────────────────────────────────────────────────

func extractLatestUserMessage2(messages []map[string]string) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i]["role"] == "user" {
			return messages[i]["content"]
		}
	}
	return ""
}

func renderEntities(items []string) string {
	if len(items) == 0 {
		return `<span style="color:#484f58;font-size:12px">none detected</span>`
	}
	out := ""
	for _, item := range items {
		item = strings.Trim(item, "*#`_~")
		if strings.ContainsAny(item, "(){}[]<>|=+/") || len(item) == 0 {
			continue
		}
		out += fmt.Sprintf(`<span class="tag">%s</span>`, item)
	}
	return out
}

func renderFallbackEvents(events []FallbackEvent) string {
	if len(events) == 0 {
		return `<p class="empty">No fallbacks this session</p>`
	}
	out := ""
	for _, e := range events {
		reason := e.Reason
		if reason == "" {
			reason = "unknown"
		}
		out += fmt.Sprintf(`<div class="fb">
  <span class="fb-icon">&#x26A1;</span>
  <div style="flex:1">
    <div class="fb-title">Provider switched <span class="fb-time">%s</span></div>
    <div class="fb-detail" style="margin-bottom:4px">
      <span class="pill from">%s</span>
      <span style="color:#484f58;margin:0 5px">&#x2192;</span>
      <span class="pill to">%s</span>
    </div>
    <div class="fb-detail">Reason: %s</div>
  </div>
</div>`, e.At.Format("15:04:05"), e.FromProvider, e.ToProvider, reason)
	}
	return out
}

func renderOpenLoopsNew(loops []string) string {
	if len(loops) == 0 {
		return `<p class="empty">No open loops detected</p>`
	}
	out := ""
	for _, l := range loops {
		l = strings.Trim(l, "*#`_~0123456789. ")
		if l == "" {
			continue
		}
		out += fmt.Sprintf(`<div class="loop open"><span class="loop-dot open"></span><span class="loop-text open">%s</span></div>`, l)
	}
	return out
}

func renderResolvedLoops(loops []string) string {
	if len(loops) == 0 {
		return `<p class="empty">No resolved loops yet</p>`
	}
	out := ""
	for _, l := range loops {
		l = strings.Trim(l, "*#`_~0123456789. ")
		if l == "" {
			continue
		}
		out += fmt.Sprintf(`<div class="loop resolved"><span class="loop-dot resolved"></span><span class="loop-text resolved">%s</span></div>`, l)
	}
	return out
}

func renderCompleted(steps []string) string {
	if len(steps) == 0 {
		return `<p class="empty">No completed steps tracked yet</p>`
	}
	out := ""
	for i, s := range steps {
		out += fmt.Sprintf(`<div class="step"><span class="snum">%02d</span><span style="color:#c9d1d9;line-height:1.4">%s</span><span class="scheck">&#x2713;</span></div>`, i+1, s)
	}
	return out
}

func renderTranscript(entries []MessageEntry) string {
	if len(entries) == 0 {
		return `<p class="empty">No messages yet</p>`
	}
	lastProvider := ""
	out := ""
	for _, e := range entries {
		ts := e.At.Format("15:04:05")
		content := e.Content
		if len(content) > 600 {
			content = content[:600] + "..."
		}

		if e.Role == "fallback" {
			out += fmt.Sprintf(`<div class="turn fallback-event">
  <div class="turn-header"><span class="trole fallback">&#x26A1; Fallback</span><span class="ttime">%s</span></div>
  <div class="turn-text fb">%s &#x2192; %s</div>
</div>`, ts, e.Provider, content)
			continue
		}

		providerBadge := ""
		if e.Role == "assistant" && e.Provider != "" {
			cls := "tprovider"
			if e.Provider != lastProvider && lastProvider != "" {
				cls = "tprovider new"
			}
			providerBadge = fmt.Sprintf(`<span class="%s">%s</span>`, cls, e.Provider)
			lastProvider = e.Provider
		}

		roleClass := e.Role
		roleLabel := strings.ToUpper(e.Role)
		out += fmt.Sprintf(`<div class="turn %s">
  <div class="turn-header"><span class="trole %s">%s</span>%s<span class="ttime">%s</span></div>
  <div class="turn-text">%s</div>
</div>`, roleClass, roleClass, roleLabel, providerBadge, ts, content)
	}
	return out
}

// ── Dashboard HTML Template ───────────────────────────────────────────────────

var dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<title>Trooper — %s</title>
<style>
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:monospace;background:linear-gradient(135deg,#0f1117 0%%,#131928 50%%,#0d1117 100%%);min-height:100vh;color:#c9d1d9;font-size:13px;position:relative}
body::before{content:'';position:fixed;top:0;left:0;right:0;bottom:0;background:radial-gradient(ellipse at 20%% 10%%,rgba(56,139,255,0.08) 0%%,transparent 50%%),radial-gradient(ellipse at 80%% 80%%,rgba(63,185,80,0.05) 0%%,transparent 50%%);pointer-events:none;z-index:0}
.wrap{position:relative;z-index:1;max-width:1200px;margin:0 auto}
.topbar{backdrop-filter:blur(20px);background:rgba(22,27,34,0.8);border-bottom:1px solid rgba(48,54,61,0.6);padding:12px 20px;display:flex;align-items:center;gap:12px}
.logo{font-size:15px;font-weight:700;color:#58a6ff;letter-spacing:-0.3px}
.live-badge{display:flex;align-items:center;gap:5px;background:rgba(63,185,80,0.12);border:1px solid rgba(63,185,80,0.25);border-radius:20px;padding:2px 10px;font-size:11px;color:#3fb950}
.dot{width:5px;height:5px;border-radius:50%%;background:#3fb950;animation:pulse 1.8s ease-in-out infinite}
@keyframes pulse{0%%,100%%{opacity:1;transform:scale(1)}50%%{opacity:0.4;transform:scale(0.8)}}
.session-id{font-size:11px;color:#6e7681;margin-left:auto}
.metrics-strip{backdrop-filter:blur(12px);background:rgba(22,27,34,0.6);border-bottom:1px solid rgba(48,54,61,0.4);padding:0 20px;display:flex;align-items:stretch;flex-wrap:wrap}
.metric{display:flex;flex-direction:column;justify-content:center;gap:3px;padding:10px 24px 10px 0;border-right:1px solid rgba(48,54,61,0.4);margin-right:24px}
.metric:last-child{border-right:none;margin-right:0}
.mlabel{font-size:9px;color:#6e7681;text-transform:uppercase;letter-spacing:1px}
.mval{font-size:13px;font-weight:600;color:#e6edf3;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;max-width:260px}
.mval.intent{color:#79c0ff;max-width:800px}
.mval.green{color:#3fb950}
.mval.blue{color:#58a6ff}
.mval.orange{color:#f0883e}
.entities{padding:8px 20px;background:rgba(13,17,23,0.4);border-bottom:1px solid rgba(33,38,45,0.6);display:flex;align-items:center;gap:6px;flex-wrap:wrap}
.elabel{font-size:9px;color:#6e7681;text-transform:uppercase;letter-spacing:1px;margin-right:2px}
.tag{font-size:10px;padding:2px 8px;border-radius:20px;background:rgba(56,139,255,0.1);color:#79c0ff;border:1px solid rgba(56,139,255,0.2)}
.grid{display:grid;grid-template-columns:1fr 1fr}
.full{grid-column:1/-1}
.panel{backdrop-filter:blur(8px);background:rgba(22,27,34,0.5);border-bottom:1px solid rgba(33,38,45,0.5);border-right:1px solid rgba(33,38,45,0.5);padding:14px 18px}
.panel.no-right{border-right:none}
.panel.no-bottom{border-bottom:none}
.ph{display:flex;align-items:center;gap:8px;margin-bottom:11px}
.ph-icon{width:22px;height:22px;border-radius:6px;display:flex;align-items:center;justify-content:center;font-size:11px;flex-shrink:0}
.ph-icon.orange{background:rgba(240,136,62,0.15);border:1px solid rgba(240,136,62,0.2)}
.ph-icon.yellow{background:rgba(210,153,34,0.15);border:1px solid rgba(210,153,34,0.2)}
.ph-icon.green{background:rgba(63,185,80,0.15);border:1px solid rgba(63,185,80,0.2)}
.ph-icon.blue{background:rgba(88,166,255,0.15);border:1px solid rgba(88,166,255,0.2)}
.ph-icon.purple{background:rgba(139,92,246,0.15);border:1px solid rgba(139,92,246,0.2)}
.pt{font-size:10px;font-weight:600;text-transform:uppercase;letter-spacing:1px;color:#8b949e}
.pc{margin-left:auto;font-size:10px;background:rgba(33,38,45,0.8);color:#6e7681;padding:1px 7px;border-radius:10px;border:1px solid rgba(48,54,61,0.4)}
.fb{background:rgba(240,136,62,0.06);border:1px solid rgba(240,136,62,0.18);border-radius:8px;padding:10px 12px;margin-bottom:6px;display:flex;gap:10px;align-items:flex-start}
.fb-icon{font-size:13px;flex-shrink:0;margin-top:1px}
.fb-title{font-size:12px;color:#f0883e;font-weight:600;display:flex;align-items:center;gap:6px;margin-bottom:4px}
.fb-detail{font-size:11px;color:#8b949e;line-height:1.5}
.pill{display:inline-block;font-size:10px;padding:1px 8px;border-radius:10px;font-weight:600}
.pill.from{background:rgba(33,38,45,0.8);color:#8b949e;border:1px solid rgba(48,54,61,0.5)}
.pill.to{background:rgba(88,166,255,0.12);color:#79c0ff;border:1px solid rgba(88,166,255,0.25)}
.fb-time{font-size:10px;color:#6e7681;margin-left:auto}
.loop{display:flex;align-items:flex-start;gap:8px;padding:7px 10px;border-radius:7px;margin-bottom:5px;font-size:12px;line-height:1.4}
.loop.open{background:rgba(210,153,34,0.07);border:1px solid rgba(210,153,34,0.15)}
.loop.resolved{background:rgba(63,185,80,0.05);border:1px solid rgba(63,185,80,0.12)}
.loop-dot{width:5px;height:5px;border-radius:50%%;margin-top:4px;flex-shrink:0}
.loop-dot.open{background:#d29922}
.loop-dot.resolved{background:#3fb950}
.loop-text.open{color:#e3b341}
.loop-text.resolved{color:#56d364;text-decoration:line-through;opacity:0.7}
.step{display:flex;align-items:flex-start;gap:8px;padding:6px 0;border-bottom:1px solid rgba(33,38,45,0.5);font-size:12px}
.step:last-child{border-bottom:none}
.snum{font-size:10px;color:#6e7681;min-width:18px;margin-top:1px}
.scheck{margin-left:auto;color:#3fb950;font-size:11px;flex-shrink:0;background:rgba(63,185,80,0.1);border:1px solid rgba(63,185,80,0.2);border-radius:4px;padding:0 4px}
.transcript{display:flex;flex-direction:column;gap:7px}
.turn{padding:9px 12px;border-radius:8px}
.turn.user{background:rgba(22,27,34,0.7);border:1px solid rgba(48,54,61,0.5)}
.turn.assistant{background:rgba(56,139,255,0.04);border:1px solid rgba(56,139,255,0.12)}
.turn.fallback-event{background:rgba(240,136,62,0.05);border:1px solid rgba(240,136,62,0.2)}
.turn-header{display:flex;align-items:center;gap:8px;margin-bottom:5px}
.trole{font-size:10px;font-weight:700;text-transform:uppercase;letter-spacing:0.8px}
.trole.user{color:#6e7681}
.trole.assistant{color:#79c0ff}
.trole.fallback{color:#f0883e}
.tprovider{font-size:10px;background:rgba(33,38,45,0.8);color:#6e7681;padding:1px 7px;border-radius:10px;border:1px solid rgba(48,54,61,0.4)}
.tprovider.new{background:rgba(56,139,255,0.1);color:#79c0ff;border:1px solid rgba(56,139,255,0.25)}
.ttime{font-size:10px;color:#6e7681;margin-left:auto}
.turn-text{font-size:12px;color:#c9d1d9;line-height:1.5;white-space:pre-wrap;word-break:break-word}
.turn-text.fb{color:#f0883e;font-size:11px}
.empty{color:#484f58;font-size:12px;padding:4px 0}
.escalate-bar{display:flex;gap:6px;margin-top:8px;padding-top:8px;border-top:1px solid rgba(48,54,61,0.4)}
.escalate-btn{font-size:10px;padding:3px 10px;border-radius:6px;border:1px solid rgba(88,166,255,0.3);background:rgba(88,166,255,0.08);color:#79c0ff;cursor:pointer;font-family:monospace}
.escalate-btn:hover{background:rgba(88,166,255,0.18);border-color:rgba(88,166,255,0.5)}
.escalate-btn.accept{border-color:rgba(63,185,80,0.3);background:rgba(63,185,80,0.08);color:#3fb950}
.escalate-btn.accept:hover{background:rgba(63,185,80,0.18)}
.footer{color:#484f58;font-size:11px;padding:10px 20px;border-top:1px solid rgba(33,38,45,0.5);backdrop-filter:blur(8px);background:rgba(13,17,23,0.4)}
</style>
</head>
<body>
<div class="wrap">
<div class="topbar">
  <span class="logo">&#x1FA96; Trooper</span>
  <span class="live-badge"><span class="dot"></span>live</span>
  <span class="session-id">%s</span>
</div>

<div class="metrics-strip">
  <div class="metric">
    <span class="mlabel">Intent</span>
    <span class="mval intent" title="%s">%s</span>
  </div>
  <div class="metric">
    <span class="mlabel">Provider</span>
    <span class="mval blue">%s</span>
  </div>
  <div class="metric">
    <span class="mlabel">Tokens saved</span>
    <span class="mval">%d</span>
  </div>
  <div class="metric">
    <span class="mlabel">History compressed</span>
    <span class="mval green">%s</span>
  </div>
  <div class="metric">
    <span class="mlabel">Confidence</span>
    <span class="mval green">%s</span>
  </div>
  <div class="metric">
    <span class="mlabel">Duration</span>
    <span class="mval">%s</span>
  </div>
  <div class="metric">
    <span class="mlabel">Fallbacks</span>
    <span class="mval orange">%d</span>
  </div>
</div>

<div class="entities">
  <span class="elabel">Entities</span>
  %s
</div>

<div class="grid">
  <div class="panel">
    <div class="ph"><div class="ph-icon orange">&#x26A1;</div><span class="pt">Fallback events</span><span class="pc">%d</span></div>
    %s
  </div>

  <div class="panel no-right">
    <div class="ph"><div class="ph-icon blue">&#x1F4CB;</div><span class="pt">Completed steps</span><span class="pc">%d</span></div>
    %s
  </div>

  <div class="panel">
    <div class="ph"><div class="ph-icon yellow">&#x1F513;</div><span class="pt">Open loops</span><span class="pc">%d</span></div>
    %s
  </div>

  <div class="panel no-right">
    <div class="ph"><div class="ph-icon green">&#x2705;</div><span class="pt">Resolved loops</span><span class="pc">%d</span></div>
    %s
  </div>

  <div class="panel full no-right">
    <div class="ph"><div class="ph-icon green">&#x1F4E1;</div><span class="pt">Session SITREP</span></div>
    <pre style="font-size:11px;color:#79c0ff;white-space:pre-wrap;line-height:1.6;margin:0">%s</pre>
  </div>

  <div class="panel full no-right no-bottom">
    <div class="ph"><div class="ph-icon purple">&#x1F4DC;</div><span class="pt">Session transcript</span></div>
    <div class="transcript">%s</div>
  </div>
</div>

<div class="footer">Auto-refreshes every 5s &nbsp;&middot;&nbsp; zero instrumentation &nbsp;&middot;&nbsp; local &amp; private</div>
</div>
<script>
function escalate(sessionId, turnId, provider) {
  fetch('/escalate/' + sessionId + '/' + turnId, {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({provider: provider})
  }).then(() => location.reload());
}
setTimeout(() => location.reload(), 5000);
</script>
</body>
</html>`
