package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"time"

	"github.com/croutoncreations/redline/internal/apiclient"
)

// runDashboard shares the native app's local session bootstrap. Never print the
// credential-bearing URL: the server removes it when it sets the session cookie.
func runDashboard(client apiclient.Client, args []string, stdout, stderr io.Writer, open func(string) error) int {
	flags := flag.NewFlagSet("dashboard", flag.ContinueOnError)
	flags.SetOutput(stderr)
	if err := flags.Parse(args); err != nil {
		return 1
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: redline [--config FILE] [--api URL] dashboard")
		return 1
	}
	address, err := url.Parse(client.BaseURL)
	if err != nil || address.Scheme != "http" || address.User != nil || address.RawQuery != "" || address.Fragment != "" || (address.Path != "" && address.Path != "/") {
		fmt.Fprintln(stderr, "dashboard requires an HTTP loopback API URL without credentials, a path, or a query")
		return 1
	}
	host := address.Hostname()
	ip := net.ParseIP(host)
	if !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
		fmt.Fprintln(stderr, "dashboard only opens a local loopback service; use `redline pair` for remote devices")
		return 1
	}
	if client.Token == "" {
		fmt.Fprintln(stderr, "Redline's API token was not found. Start the service, then use the same --config path with `redline dashboard`.")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Do(ctx, http.MethodGet, "/v1/health", nil, nil); err != nil {
		// Transport errors can include URLs. Only report the token-free origin.
		fmt.Fprintf(stderr, "Cannot reach the authenticated Redline service at %s. Start it and check --api and --config.\n", address.String())
		return 1
	}
	address.Path = "/"
	origin := address.String()
	query := url.Values{"access_token": {client.Token}}
	address.RawQuery = query.Encode()
	if err := open(address.String()); err != nil {
		// An OS browser-launch error may echo its arguments, including the token.
		fmt.Fprintln(stderr, "Could not open the default browser. Run this command from a local desktop session.")
		return 1
	}
	fmt.Fprintf(stdout, "Opened the Redline dashboard at %s\n", origin)
	return 0
}

// Copyable command arguments for the host's usual shell (PowerShell on Windows).
func shellQuote(value string) string {
	if runtime.GOOS == "windows" {
		return "'" + strings.ReplaceAll(value, "'", "''") + "'"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

// A wildcard listener is not a browser destination. Use the corresponding local
// address in the suggested command while preserving the actual listen endpoint.
func dashboardOrigin(listenAddress string) string {
	host, port, err := net.SplitHostPort(listenAddress)
	if err != nil {
		return "http://" + listenAddress
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		if ip.To4() != nil {
			host = "127.0.0.1"
		} else {
			host = "::1"
		}
	} else if host == "" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}
