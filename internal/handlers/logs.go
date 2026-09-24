package handlers

import (
	"bufio"
	"fmt"
	"html"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"

	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/config"
	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/runner"
)

// HandleLogsView handles the web log viewer, SSE stream, and raw CLI text endpoints with RBAC protection
func HandleLogsView(w http.ResponseWriter, r *http.Request, host string, cfg *config.Config, getPortFunc func(string) int) {
	if appParam := r.URL.Query().Get("app"); appParam != "" {
		host = appParam
	}

	// Handle logout
	if r.URL.Query().Get("logout") == "1" {
		http.SetCookie(w, &http.Cookie{
			Name:     "kube_token",
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: true,
		})
		http.Redirect(w, r, "/_logs?app="+url.QueryEscape(host), http.StatusSeeOther)
		return
	}

	// Extract token from: Authorization header, ?token= param, or kube_token cookie
	token := ""
	authHeader := r.Header.Get("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") {
		token = strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))
	} else if qToken := r.URL.Query().Get("token"); qToken != "" {
		token = strings.TrimSpace(qToken)
	} else if cookie, err := r.Cookie("kube_token"); err == nil {
		token = strings.TrimSpace(cookie.Value)
	}

	// Handle form token submission
	if r.Method == http.MethodPost && r.FormValue("token") != "" {
		token = strings.TrimSpace(r.FormValue("token"))
	}

	// Verify token against Kubernetes RBAC via SelfSubjectAccessReview
	allowed, reason, err := runner.VerifyKubeToken(token, cfg.KubeNamespace)
	if err != nil {
		log.Printf("Token verification error: %v", err)
		http.Error(w, "Failed to verify Kubernetes token: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if !allowed {
		isRaw := r.URL.Query().Get("raw") == "1" || strings.Contains(r.Header.Get("Accept"), "text/plain")
		isStream := r.URL.Query().Get("stream") == "1" || r.URL.Path == "/_logs/stream"
		if isRaw || isStream {
			w.Header().Set("WWW-Authenticate", `Bearer realm="kubernetes"`)
			http.Error(w, fmt.Sprintf("HTTP 401 Unauthorized: Valid Kubernetes token required (%s).\n\nUsage:\n  TOKEN=$(kubectl create token default)\n  curl -H \"Authorization: Bearer $TOKEN\" \"https://%s/_logs?raw=1\"\n", reason, host), http.StatusUnauthorized)
			return
		}

		// Render interactive token login page for browser users
		renderAuthPage(w, host, cfg, reason)
		return
	}

	// If token came from form POST or query param, set cookie for seamless session
	if r.Method == http.MethodPost || r.URL.Query().Get("token") != "" {
		http.SetCookie(w, &http.Cookie{
			Name:     "kube_token",
			Value:    token,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
		})
		if r.Method == http.MethodPost {
			http.Redirect(w, r, "/_logs?app="+url.QueryEscape(host), http.StatusSeeOther)
			return
		}
	}

	unitName := fmt.Sprintf("app-%s", host)
	cliCmd := fmt.Sprintf("kubectl exec -n default deployment/zero-scale-gateway -- journalctl -u %s -f", unitName)

	// Raw output for authenticated curl users
	if r.URL.Query().Get("raw") == "1" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		out, err := exec.Command("journalctl", "-u", unitName, "-n", "250", "--no-pager").CombinedOutput()
		if err != nil {
			fmt.Fprintf(w, "Error querying journalctl for %s: %v\n", unitName, err)
			return
		}
		w.Write(out)
		return
	}

	// SSE streaming endpoint for live logs
	if r.URL.Query().Get("stream") == "1" || r.URL.Path == "/_logs/stream" {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Access-Control-Allow-Origin", "*")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
			return
		}

		ctx := r.Context()
		cmd := exec.CommandContext(ctx, "journalctl", "-u", unitName, "-f", "-n", "50", "--no-pager")
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			fmt.Fprintf(w, "data: [error opening journalctl pipe: %v]\n\n", err)
			flusher.Flush()
			return
		}
		if err := cmd.Start(); err != nil {
			fmt.Fprintf(w, "data: [error starting journalctl: %v]\n\n", err)
			flusher.Flush()
			return
		}

		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			fmt.Fprintf(w, "data: %s\n\n", line)
			flusher.Flush()
		}
		_ = cmd.Wait()
		return
	}

	// HTML Web Log Viewer UI
	active, _ := runner.IsServiceActive(unitName)
	statusBadge := `<span class="badge idle">💤 SCALED TO ZERO (IDLE)</span>`
	if active {
		port := getPortFunc(host)
		if port == 0 {
			port = runner.GetActiveServicePort(unitName)
		}
		statusBadge = fmt.Sprintf(`<span class="badge active">🟢 ACTIVE (Port %d)</span>`, port)
	}

	initialLogs, _ := exec.Command("journalctl", "-u", unitName, "-n", "100", "--no-pager").CombinedOutput()
	if len(initialLogs) == 0 {
		initialLogs = []byte("-- No log entries recorded yet for " + unitName + " --\n")
	}

	var appChips strings.Builder
	if entries, err := os.ReadDir(cfg.AppsDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() && !strings.HasSuffix(e.Name(), ".type") && e.Name() != "lost+found" {
				currHost := e.Name()
				activeCls := ""
				if currHost == host {
					activeCls = " active"
				}
				appChips.WriteString(fmt.Sprintf(`<a href="/_logs?app=%s" class="chip%s">%s</a>`, currHost, activeCls, html.EscapeString(currHost)))
			}
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageHTML := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>%s - Zero-Scale Service Logs</title>
<style>
  :root {
    --bg: #0d1117;
    --card: #161b22;
    --border: #30363d;
    --text: #c9d1d9;
    --muted: #8b949e;
    --green: #2ea043;
    --amber: #d29922;
    --blue: #58a6ff;
    --red: #f85149;
    --term-bg: #06090f;
  }
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body {
    background-color: var(--bg);
    color: var(--text);
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif;
    padding: 24px;
    line-height: 1.5;
  }
  .container { max-width: 1200px; margin: 0 auto; }
  header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding-bottom: 16px;
    border-bottom: 1px solid var(--border);
    margin-bottom: 20px;
    flex-wrap: wrap;
    gap: 12px;
  }
  h1 { font-size: 20px; font-weight: 600; display: flex; align-items: center; gap: 10px; }
  .badge {
    font-size: 12px;
    padding: 4px 10px;
    border-radius: 20px;
    font-weight: 600;
  }
  .badge.active { background: rgba(46, 160, 67, 0.15); color: #3fb950; border: 1px solid rgba(46, 160, 67, 0.4); }
  .badge.idle { background: rgba(210, 153, 34, 0.15); color: #e3b341; border: 1px solid rgba(210, 153, 34, 0.4); }
  .badge.rbac { background: rgba(88, 166, 255, 0.15); color: #58a6ff; border: 1px solid rgba(88, 166, 255, 0.4); }
  .links { display: flex; gap: 10px; flex-wrap: wrap; }
  .btn {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    background: var(--card);
    color: var(--text);
    text-decoration: none;
    padding: 6px 12px;
    border-radius: 6px;
    border: 1px solid var(--border);
    font-size: 13px;
    cursor: pointer;
    transition: all 0.15s ease;
  }
  .btn:hover { background: #21262d; border-color: #8b949e; color: #fff; }
  .btn-primary { background: #238636; border-color: rgba(240, 246, 252, 0.1); color: #fff; }
  .btn-primary:hover { background: #2ea043; border-color: rgba(240, 246, 252, 0.2); }
  .btn-danger { color: #f85149; }
  .btn-danger:hover { background: rgba(248, 81, 73, 0.1); border-color: #f85149; }
  .chips { margin-bottom: 16px; display: flex; gap: 8px; flex-wrap: wrap; align-items: center; }
  .chip-label { font-size: 12px; color: var(--muted); text-transform: uppercase; font-weight: 600; margin-right: 4px; }
  .chip {
    padding: 4px 10px;
    border-radius: 12px;
    background: var(--card);
    color: var(--muted);
    border: 1px solid var(--border);
    font-size: 12px;
    text-decoration: none;
  }
  .chip:hover { color: var(--text); border-color: var(--muted); }
  .chip.active { background: #1f6feb22; border-color: var(--blue); color: var(--blue); font-weight: 600; }
  .cli-box {
    background: var(--card);
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 10px 14px;
    margin-bottom: 16px;
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 12px;
    font-family: ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, monospace;
    font-size: 13px;
  }
  .cli-code { color: #79c0ff; overflow-x: auto; white-space: nowrap; }
  .terminal-card {
    background: var(--term-bg);
    border: 1px solid var(--border);
    border-radius: 8px;
    overflow: hidden;
    box-shadow: 0 10px 24px rgba(0,0,0,0.5);
  }
  .terminal-header {
    background: #161b22;
    padding: 8px 14px;
    border-bottom: 1px solid var(--border);
    display: flex;
    justify-content: space-between;
    align-items: center;
    font-size: 12px;
    color: var(--muted);
  }
  .term-controls { display: flex; gap: 8px; align-items: center; }
  .term-dot { width: 10px; height: 10px; border-radius: 50%%; display: inline-block; }
  .dot-green { background: #3fb950; box-shadow: 0 0 6px #3fb950; }
  pre#logs {
    padding: 16px;
    font-family: ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, monospace;
    font-size: 12px;
    line-height: 1.6;
    color: #e6edf3;
    overflow-y: auto;
    max-height: 65vh;
    white-space: pre-wrap;
    word-break: break-all;
  }
</style>
</head>
<body>
<div class="container">
  <header>
    <div>
      <h1>⚡ %s %s <span class="badge rbac">🔒 RBAC AUTHENTICATED</span></h1>
      <div style="font-size: 12px; color: var(--muted); margin-top: 4px;">Zero-Scale Isolated Journald Logs</div>
    </div>
    <div class="links">
      <a href="https://%s/" target="_blank" class="btn btn-primary">🌐 Open App</a>
      <a href="%s" target="_blank" class="btn">☸️ Kube Logs</a>
      <a href="%s" target="_blank" class="btn">☸️ Pod Shell</a>
      <a href="/_logs?app=%s&raw=1" target="_blank" class="btn">📄 Raw Text</a>
      <a href="/_logs?app=%s&logout=1" class="btn btn-danger">🔒 Lock Session</a>
    </div>
  </header>

  <div class="chips">
    <span class="chip-label">Services:</span>
    %s
  </div>

  <div class="cli-box">
    <div class="cli-code"><span style="color:var(--muted)">$</span> %s</div>
    <button class="btn" id="copy-btn">Copy CLI</button>
  </div>

  <div class="terminal-card">
    <div class="terminal-header">
      <div style="display:flex; align-items:center; gap:8px;">
        <span class="term-dot dot-green" id="stream-dot"></span>
        <span id="stream-status">Live Stream Active</span>
      </div>
      <div class="term-controls">
        <button class="btn" id="stream-toggle">Pause Stream</button>
        <button class="btn" id="clear-btn">Clear</button>
      </div>
    </div>
    <pre id="logs">%s</pre>
  </div>
</div>

<script>
  const host = "%s";
  const cliCmd = "%s";
  const logElem = document.getElementById("logs");
  const dotElem = document.getElementById("stream-dot");
  const statusElem = document.getElementById("stream-status");
  const toggleBtn = document.getElementById("stream-toggle");
  const clearBtn = document.getElementById("clear-btn");
  const copyBtn = document.getElementById("copy-btn");

  logElem.scrollTop = logElem.scrollHeight;

  copyBtn.addEventListener("click", function() {
    navigator.clipboard.writeText(cliCmd);
    copyBtn.innerText = "Copied!";
    setTimeout(() => copyBtn.innerText = "Copy CLI", 2000);
  });

  clearBtn.addEventListener("click", function() {
    logElem.textContent = "";
  });

  let evtSource = null;
  let isStreaming = false;

  function startStream() {
    evtSource = new EventSource("/_logs?app=" + encodeURIComponent(host) + "&stream=1");
    evtSource.onmessage = function(e) {
      logElem.textContent += e.data + "\n";
      logElem.scrollTop = logElem.scrollHeight;
    };
    evtSource.onerror = function() {
      statusElem.innerText = "Reconnecting...";
      dotElem.style.background = "#d29922";
    };
    evtSource.onopen = function() {
      statusElem.innerText = "Live Stream Active";
      dotElem.style.background = "#3fb950";
    };
    isStreaming = true;
    toggleBtn.innerText = "Pause Stream";
  }

  function stopStream() {
    if (evtSource) {
      evtSource.close();
      evtSource = null;
    }
    isStreaming = false;
    statusElem.innerText = "Stream Paused";
    dotElem.style.background = "#8b949e";
    toggleBtn.innerText = "Resume Stream";
  }

  toggleBtn.addEventListener("click", function() {
    if (isStreaming) stopStream();
    else startStream();
  });

  startStream();
</script>
</body>
</html>`,
		html.EscapeString(host),
		html.EscapeString(host),
		statusBadge,
		html.EscapeString(host),
		cfg.KubeLogURL(),
		cfg.KubeShellURL(),
		url.QueryEscape(host),
		url.QueryEscape(host),
		appChips.String(),
		html.EscapeString(cliCmd),
		html.EscapeString(string(initialLogs)),
		html.EscapeString(host),
		cliCmd,
	)

	w.Write([]byte(pageHTML))
}

func renderAuthPage(w http.ResponseWriter, host string, cfg *config.Config, reason string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)

	errorNotice := ""
	if reason != "" && reason != "Missing token" {
		errorNotice = fmt.Sprintf(`<div style="background:rgba(248,81,73,0.15); border:1px solid #f85149; color:#ff7b72; padding:10px 14px; border-radius:6px; margin-bottom:16px; font-size:13px;">⚠️ %s</div>`, html.EscapeString(reason))
	}

	htmlPage := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Authentication Required - Zero-Scale Logs</title>
<style>
  :root {
    --bg: #0d1117;
    --card: #161b22;
    --border: #30363d;
    --text: #c9d1d9;
    --muted: #8b949e;
    --blue: #58a6ff;
  }
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body {
    background-color: var(--bg);
    color: var(--text);
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif;
    display: flex;
    align-items: center;
    justify-content: center;
    min-height: 100vh;
    padding: 20px;
  }
  .auth-card {
    background: var(--card);
    border: 1px solid var(--border);
    border-radius: 10px;
    padding: 30px;
    max-width: 500px;
    width: 100%%;
    box-shadow: 0 16px 36px rgba(0,0,0,0.6);
  }
  h1 { font-size: 20px; margin-bottom: 8px; display: flex; align-items: center; gap: 8px; }
  p { font-size: 14px; color: var(--muted); margin-bottom: 20px; line-height: 1.5; }
  .code-hint {
    background: #06090f;
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 10px 12px;
    font-family: ui-monospace, SFMono-Regular, "SF Mono", monospace;
    font-size: 13px;
    color: #79c0ff;
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 20px;
  }
  .copy-btn {
    background: transparent;
    border: 1px solid var(--border);
    color: var(--muted);
    padding: 4px 8px;
    border-radius: 4px;
    font-size: 11px;
    cursor: pointer;
  }
  .copy-btn:hover { color: #fff; border-color: var(--muted); }
  input[type="password"], input[type="text"] {
    width: 100%%;
    background: #06090f;
    border: 1px solid var(--border);
    color: var(--text);
    padding: 10px 12px;
    border-radius: 6px;
    font-size: 13px;
    font-family: monospace;
    margin-bottom: 16px;
  }
  input:focus { outline: none; border-color: var(--blue); }
  button.submit-btn {
    width: 100%%;
    background: #238636;
    color: #fff;
    border: none;
    padding: 10px;
    border-radius: 6px;
    font-weight: 600;
    font-size: 14px;
    cursor: pointer;
    transition: background 0.15s ease;
  }
  button.submit-btn:hover { background: #2ea043; }
</style>
</head>
<body>
<div class="auth-card">
  <h1>🔒 Kubernetes RBAC Required</h1>
  <p>To view logs for <strong>%s</strong>, you must provide a valid Kubernetes token with log viewing permissions in namespace <code>%s</code>.</p>

  %s

  <div style="font-size: 12px; color: var(--muted); margin-bottom: 6px; font-weight: 600;">GENERATE A TOKEN VIA KUBECTL:</div>
  <div class="code-hint">
    <span>kubectl create token default</span>
    <button class="copy-btn" onclick="navigator.clipboard.writeText('kubectl create token default'); this.innerText='Copied!'; setTimeout(()=>this.innerText='Copy', 1500)">Copy</button>
  </div>

  <form method="POST" action="/_logs?app=%s">
    <div style="font-size: 12px; color: var(--muted); margin-bottom: 6px; font-weight: 600;">ENTER KUBERNETES TOKEN:</div>
    <input type="password" name="token" placeholder="eyJhbGciOiJSUzI1NiIsImtpZCI6..." required autofocus autocomplete="off" />
    <button type="submit" class="submit-btn">Authorize & View Logs</button>
  </form>
</div>
</body>
</html>`,
		html.EscapeString(host),
		html.EscapeString(cfg.KubeNamespace),
		errorNotice,
		url.QueryEscape(host),
	)

	w.Write([]byte(htmlPage))
}
