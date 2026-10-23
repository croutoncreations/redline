package cli

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/croutoncreations/redline/internal/api"
	"github.com/croutoncreations/redline/internal/apiclient"
	"github.com/croutoncreations/redline/internal/decision"
	"github.com/croutoncreations/redline/internal/nativeusage"
	"github.com/mattn/go-isatty"
)

func runResetRedeem(client apiclient.Client, args []string, stdin io.Reader, stdout, stderr io.Writer, now func() time.Time) int {
	flags := flag.NewFlagSet("reset redeem", flag.ContinueOnError)
	flags.SetOutput(stderr)
	provider := flags.String("provider", "", "provider account id")
	dryRun := flags.Bool("dry-run", false, "validate the provider request using a nonexistent reset id")
	jsonOutput := flags.Bool("json", false, "print JSON")
	yes := flags.Bool("yes", false, "confirm spending a one-time reset")
	if err := flags.Parse(args); err != nil {
		return 1
	}
	if *provider == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: redline reset redeem --provider <id> [--dry-run] [--json] [--yes]")
		return 1
	}
	path := "/v1/providers/" + url.PathEscape(*provider)
	body := map[string]any{"dry_run": *dryRun}
	if !*dryRun && !*yes {
		file, ok := stdin.(*os.File)
		if !ok || !isatty.IsTerminal(file.Fd()) {
			fmt.Fprintln(stderr, "spending a reset requires a TTY for confirmation; use --yes or --dry-run")
			return 1
		}
		var snapshot decision.UsageSnapshot
		if err := client.Do(context.Background(), http.MethodGet, path+"/status", nil, &snapshot); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		selected, err := api.SelectBankedReset(snapshot.Provider, snapshot.BankedResetDetails, now())
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 4
		}
		expiry := "unknown"
		if selected.ExpiresAt != nil {
			expiry = selected.ExpiresAt.UTC().Format(time.RFC3339)
		}
		clears := strings.Join(selected.Clears, ", ")
		if clears == "" {
			clears = "unknown windows"
		}
		fmt.Fprintf(stderr, "Provider: %s\nReset: %s\nExpires: %s\nClears: %s\nThis spends a one-time reset. Continue? [y/N] ", *provider, selected.ID, expiry, clears)
		answer, _ := bufio.NewReader(stdin).ReadString('\n')
		if answer = strings.ToLower(strings.TrimSpace(answer)); answer != "y" && answer != "yes" {
			fmt.Fprintln(stderr, "Cancelled; no reset spent.")
			return 1
		}
		body["expected_reset_id"] = selected.ID
	}
	// Redemption plus the post-redeem usage refresh can exceed the default
	// API client's timeout. Each provider operation still has its own bound.
	client.HTTPClient = &http.Client{Timeout: 60 * time.Second}
	var result nativeusage.RedeemResult
	if err := client.Do(context.Background(), http.MethodPost, path+"/banked-resets/redeem", body, &result); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *jsonOutput {
		writeJSON(stdout, result)
	} else {
		if result.Replayed {
			fmt.Fprintf(stdout, "%s: already redeemed", *provider)
			if result.CompletedAt != nil {
				fmt.Fprintf(stdout, " at %s", result.CompletedAt.UTC().Format(time.RFC3339))
			}
			fmt.Fprintln(stdout)
			return 0
		}
		fmt.Fprintf(stdout, "%s: %s", *provider, result.Outcome)
		if result.Code != "" {
			fmt.Fprintf(stdout, " (%s)", result.Code)
		}
		if result.Reason != "" {
			fmt.Fprintf(stdout, " — %s", result.Reason)
		}
		if result.CooldownUntil != nil {
			fmt.Fprintf(stdout, " until %s", result.CooldownUntil.UTC().Format(time.RFC3339))
		}
		if result.Outcome == "redeemed" {
			fmt.Fprintf(stdout, "; refill verified: %t", result.Verified)
		}
		fmt.Fprintln(stdout)
	}
	switch result.Outcome {
	case "redeemed", "dry_run_ok":
		return 0
	case "nothing_to_reset":
		return 3
	case "cooldown", "unavailable":
		return 4
	case "gone":
		return 5
	default:
		return 1
	}
}
