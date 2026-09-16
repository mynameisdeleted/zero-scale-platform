package handlers

import (
	"bufio"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"

	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/config"
	"git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform/internal/runner"
)

// HandleLogsView handles the web log viewer, SSE stream, and raw CLI text endpoints
func HandleLogsView(w http.ResponseWriter, r *http.Request, host string, cfg *config.Config, getPortFunc func(string) int) {
	if appParam := r.URL.Query().Get("app"); appParam != "" {
		host = appParam
	}
	unitName := fmt.Sprintf("app-%s", host)
	cliCmd := fmt.Sprintf("kubectl exec -n default deployment/zero-scale-gateway -- journalctl -u %s -f", unitName)

	// Raw output for curl users
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
      <h1>⚡ %s %s</h1>
      <div style="font-size: 12px; color: var(--muted); margin-top: 4px;">Zero-Scale Isolated Journald Logs</div>
    </div>
    <div class="links">
      <a href="https://%s/" target="_blank" class="btn btn-primary">🌐 Open App</a>
      <a href="%s" target="_blank" class="btn">☸️ Kube Logs</a>
      <a href="%s" target="_blank" class="btn">☸️ Pod Shell</a>
      <a href="/_logs?app=%s&raw=1" target="_blank" class="btn">📄 Raw Text</a>
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
		appChips.String(),
		html.EscapeString(cliCmd),
		html.EscapeString(string(initialLogs)),
		html.EscapeString(host),
		cliCmd,
	)

	w.Write([]byte(pageHTML))
}
