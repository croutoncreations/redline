package api

import (
	"html/template"
	"net/http"
)

var dashboardSignIn = template.Must(template.New("dashboard-signin").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Open Redline</title>
<style>body{color:#f1f2f4;background:#101215;font:16px/1.6 system-ui;margin:0;padding:8vh 24px}main{max-width:680px;margin:auto}h1{line-height:1.2}pre{white-space:pre-wrap;overflow-wrap:anywhere;background:#20242b;border:1px solid #414650;border-radius:10px;padding:20px}a{color:#ff817b}.brand{letter-spacing:.2em;color:#ff817b}</style></head>
<body><main><p class="brand">REDLINE</p><h1>Open your local Redline dashboard</h1><p>This dashboard needs a local authenticated session. Your API remains protected.</p>
{{if .Local}}<p>Run this command in Terminal on the computer running Redline:</p><pre>redline --api {{.Origin}} dashboard</pre><p>Using a custom configuration? Include the same <code>--config</code> path you used to start the service:</p><pre>redline --config /path/to/redline.yaml --api {{.Origin}} dashboard</pre><p>The command opens your browser and signs it in using the protected local credential. Do not paste your API token into this page or share it.</p>
{{else}}<p>Open Redline on its host computer and run <code>redline pair</code> to connect this device.</p><p><a href="/pair">Pair this device</a></p>{{end}}
<p>Installed the macOS app? Choose <strong>Open Dashboard</strong> from Redline’s menu bar.</p><p><a href="https://github.com/croutoncreations/redline/blob/main/docs/cli.md">Dashboard and CLI instructions</a></p></main></body></html>`))

func isDashboardPath(path string) bool {
	return path == "/" || path == "/dashboard" || path == "/m"
}

func (s *Server) serveDashboardSignIn(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(http.StatusUnauthorized)
	_ = dashboardSignIn.Execute(w, struct {
		Local  bool
		Origin string
	}{Local: loopbackHost(r.Host), Origin: "http://" + r.Host})
}
