package server

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"html"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type publicWafPageDiagnostic struct {
	Label  string
	Status string
	Tone   string
}

type publicWafPageModel struct {
	Title          string
	Heading        string
	Lead           string
	Host           string
	ReferenceID    string
	FooterLabel    string
	Diagnostics    []publicWafPageDiagnostic
	PrimaryHTML    string
	SecondaryHTML  string
	RefreshSeconds int
}

func writePublicWafInterstitialPage(w http.ResponseWriter, model publicWafPageModel) {
	title := model.Title
	if title == "" {
		title = "p2pstream security check"
	}
	footer := model.FooterLabel
	if footer == "" {
		footer = "Security by p2pstream"
	}
	referenceID := model.ReferenceID
	if referenceID == "" {
		referenceID = publicWafReferenceID(0)
	}
	refreshMeta := ""
	if model.RefreshSeconds > 0 {
		refreshMeta = fmt.Sprintf("\n  <meta http-equiv=\"refresh\" content=\"%d\">", maxInt(1, model.RefreshSeconds))
	}

	_, _ = fmt.Fprintf(w, `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">%s
  <title>%s</title>
  <style>
%s
  </style>
</head>
<body>
  <main class="cf-shell">`,
		refreshMeta,
		html.EscapeString(title),
		publicWafPageCSS(),
	)
	if model.Host != "" {
		_, _ = fmt.Fprintf(w, `
    <div class="cf-host">%s</div>`, html.EscapeString(model.Host))
	}
	_, _ = fmt.Fprintf(w, `
    <section class="cf-copy">
      <h1>%s</h1>
      <p>%s</p>
    </section>
    <div class="cf-divider" aria-hidden="true"></div>
    <section class="cf-diagnostics" aria-label="Connection diagnostics">
%s
    </section>
    <section class="cf-panel">
%s
    </section>
    <section class="cf-meta">
%s
    </section>
    <footer class="cf-footer">
      <span>%s</span>
      <span>Reference ID: %s</span>
    </footer>
  </main>
</body>
</html>`,
		html.EscapeString(model.Heading),
		html.EscapeString(model.Lead),
		publicWafDiagnosticsHTML(model.Diagnostics),
		model.PrimaryHTML,
		model.SecondaryHTML,
		html.EscapeString(footer),
		html.EscapeString(referenceID),
	)
}

func publicWafDiagnosticsHTML(diagnostics []publicWafPageDiagnostic) string {
	var b strings.Builder
	for _, diagnostic := range diagnostics {
		tone := publicWafDiagnosticTone(diagnostic.Tone)
		fmt.Fprintf(&b, `      <div class="cf-diagnostic cf-diagnostic-%s">
        <span class="cf-dot" aria-hidden="true"></span>
        <div>
          <span class="cf-label">%s</span>
          <strong>%s</strong>
        </div>
      </div>
`, tone, html.EscapeString(diagnostic.Label), html.EscapeString(diagnostic.Status))
	}
	return b.String()
}

func publicWafDiagnosticTone(tone string) string {
	switch tone {
	case "ok", "warn", "muted":
		return tone
	default:
		return "muted"
	}
}

func publicWafReferenceID(ruleID int64) string {
	buf := make([]byte, 8)
	if _, err := io.ReadFull(rand.Reader, buf); err == nil {
		return "waf-" + strconv.FormatInt(ruleID, 10) + "-" + hex.EncodeToString(buf)
	}
	return "waf-" + strconv.FormatInt(ruleID, 10) + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
}

func publicWafRequestHost(r *http.Request) string {
	if r == nil {
		return "this site"
	}
	host := normalizeRequestHost(r.Host)
	if host == "" && r.URL != nil {
		host = normalizeRequestHost(r.URL.Host)
	}
	if host == "" {
		return "this site"
	}
	return host
}

func publicWafPageCSS() string {
	return `    :root {
      color-scheme: light;
      --cf-bg: #ffffff;
      --cf-text: #1d1d1d;
      --cf-muted: #6b7280;
      --cf-border: #d9d9d9;
      --cf-accent: #f6821f;
      --cf-success: #16a34a;
      --cf-warn-text: #9a4a00;
    }
    * {
      box-sizing: border-box;
      letter-spacing: 0;
    }
    html {
      min-height: 100%;
      background: var(--cf-bg);
    }
    body {
      margin: 0;
      min-height: 100vh;
      background: var(--cf-bg);
      color: var(--cf-text);
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
      padding: 48px 24px;
    }
    .cf-shell {
      width: min(960px, 100%);
      margin: 0 auto;
    }
    .cf-host {
      margin: 0 0 24px;
      color: var(--cf-muted);
      font-size: 0.95rem;
      line-height: 1.4;
    }
    .cf-copy {
      max-width: 760px;
      padding: 36px 0 30px;
    }
    .cf-copy h1 {
      margin: 0 0 18px;
      color: var(--cf-text);
      font-size: clamp(2rem, 5vw, 3.25rem);
      font-weight: 500;
      line-height: 1.08;
    }
    .cf-copy p {
      margin: 0;
      color: var(--cf-muted);
      font-size: 1.06rem;
      line-height: 1.65;
    }
    .cf-divider {
      height: 1px;
      background: var(--cf-border);
    }
    .cf-diagnostics {
      display: grid;
      grid-template-columns: repeat(3, minmax(0, 1fr));
      border-bottom: 1px solid var(--cf-border);
    }
    .cf-diagnostic {
      display: flex;
      align-items: center;
      gap: 12px;
      min-height: 96px;
      padding: 22px 26px 22px 0;
      border-right: 1px solid var(--cf-border);
    }
    .cf-diagnostic:last-child {
      border-right: 0;
      padding-right: 0;
      padding-left: 26px;
    }
    .cf-diagnostic:nth-child(2) {
      padding-left: 26px;
    }
    .cf-dot {
      width: 14px;
      height: 14px;
      border-radius: 999px;
      flex: 0 0 auto;
      background: var(--cf-muted);
      box-shadow: inset 0 0 0 2px #ffffff;
      border: 2px solid currentColor;
      color: var(--cf-muted);
    }
    .cf-diagnostic-ok .cf-dot {
      color: var(--cf-success);
      background: var(--cf-success);
    }
    .cf-diagnostic-warn .cf-dot {
      color: var(--cf-accent);
      background: var(--cf-accent);
    }
    .cf-label {
      display: block;
      margin-bottom: 5px;
      color: var(--cf-muted);
      font-size: 0.82rem;
      line-height: 1.2;
    }
    .cf-diagnostic strong {
      display: block;
      color: var(--cf-text);
      font-size: 1rem;
      font-weight: 600;
      line-height: 1.25;
    }
    .cf-diagnostic-warn strong {
      color: var(--cf-warn-text);
    }
    .cf-panel {
      padding: 34px 0 24px;
      border-bottom: 1px solid var(--cf-border);
    }
    .cf-challenge-form {
      max-width: 420px;
    }
    .cf-widget {
      min-height: 76px;
      margin: 0 0 20px;
    }
    .cf-button {
      display: inline-flex;
      align-items: center;
      justify-content: center;
      min-height: 42px;
      border: 1px solid #c9c9c9;
      border-radius: 3px;
      background: #f7f7f7;
      color: var(--cf-text);
      font: inherit;
      font-weight: 600;
      line-height: 1;
      padding: 0 18px;
      cursor: pointer;
    }
    .cf-button:hover {
      background: #eeeeee;
    }
    .cf-noscript {
      margin: 12px 0 0;
      color: var(--cf-muted);
      line-height: 1.5;
    }
    .cf-queue-grid {
      display: grid;
      grid-template-columns: repeat(2, minmax(0, 180px));
      gap: 18px;
    }
    .cf-stat {
      border: 1px solid var(--cf-border);
      border-radius: 4px;
      padding: 16px 18px;
      background: #ffffff;
    }
    .cf-stat span {
      display: block;
      margin-bottom: 8px;
      color: var(--cf-muted);
      font-size: 0.82rem;
      line-height: 1.2;
    }
    .cf-stat strong {
      display: block;
      color: var(--cf-text);
      font-size: 2rem;
      font-weight: 500;
      line-height: 1;
    }
    .cf-meta {
      max-width: 720px;
      padding: 22px 0 34px;
      color: var(--cf-muted);
      font-size: 0.95rem;
      line-height: 1.65;
    }
    .cf-meta p {
      margin: 0;
    }
    .cf-footer {
      display: flex;
      flex-wrap: wrap;
      gap: 10px 18px;
      justify-content: space-between;
      border-top: 1px solid var(--cf-border);
      padding: 20px 0 0;
      color: var(--cf-muted);
      font-size: 0.82rem;
      line-height: 1.4;
    }
    @media (max-width: 640px) {
      body {
        padding: 28px 18px;
      }
      .cf-copy {
        padding-top: 20px;
      }
      .cf-copy h1 {
        font-size: 2rem;
      }
      .cf-diagnostics {
        grid-template-columns: 1fr;
      }
      .cf-diagnostic,
      .cf-diagnostic:nth-child(2),
      .cf-diagnostic:last-child {
        min-height: 76px;
        padding: 18px 0;
        border-right: 0;
        border-bottom: 1px solid var(--cf-border);
      }
      .cf-diagnostic:last-child {
        border-bottom: 0;
      }
      .cf-queue-grid {
        grid-template-columns: 1fr;
      }
      .cf-footer {
        display: block;
      }
      .cf-footer span {
        display: block;
        margin-bottom: 6px;
      }
    }`
}
