package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"text/tabwriter"

	"github.com/croutoncreations/redline/internal/apiclient"
)

const accountUsage = `usage: redline account <list|add|edit|disable|enable|remove|discover|profiles|add-profile>

  list                                   accounts, where each comes from, and their status
  discover <claude|codex>                OpenUsage cards on this Mac and which account reads each
  profiles [--prefix .claude-cc-]        Claude Code profile directories (CLAUDE_CONFIG_DIR) in your home folder
  add-profile <id> --dir ~/.claude-cc-NAME [--label L] [--dry-run]
                                         watch a Claude Code profile's own login (read-only); runs on this
                                         account start Claude Code with that profile
  add <id> --provider claude [--label L] [--card claude@abcd1234] [--openusage-url URL] [--source openusage|auto|native]
  edit <id> [--label L] [--card C] [--openusage-url URL] [--source S]
  disable <id> | enable <id>             keep an account on file but stop or restart watching it
  remove <id>                            forget a Redline-managed account (refused while profiles use it)

Accounts in the main config file are listed but are changed by editing that file.
Redline never signs in for you and stores no passwords or tokens. Changes apply
when the Redline service restarts.`

type cliAccount struct {
	ID            string `json:"id"`
	Provider      string `json:"provider"`
	Label         string `json:"label"`
	UsageSource   string `json:"usage_source"`
	OpenUsageCard string `json:"openusage_card"`
	ClaudeDir     string `json:"claude_config_dir"`
	Origin        string `json:"origin"`
	Disabled      bool   `json:"disabled"`
	Running       bool   `json:"running"`
	Pending       string `json:"pending"`
	Status        string `json:"status"`
	LastError     string `json:"last_error"`
	SignIn        string `json:"sign_in"`
}

type cliAccounts struct {
	Accounts        []cliAccount `json:"accounts"`
	RestartRequired bool         `json:"restart_required"`
	AccountsFile    string       `json:"accounts_file"`
}

func runAccount(client apiclient.Client, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, accountUsage)
		return 1
	}
	ctx := context.Background()
	switch args[0] {
	case "list":
		flags := flag.NewFlagSet("account list", flag.ContinueOnError)
		flags.SetOutput(stderr)
		jsonOutput := flags.Bool("json", false, "emit JSON")
		if err := flags.Parse(args[1:]); err != nil {
			return 1
		}
		var response cliAccounts
		if err := client.Do(ctx, http.MethodGet, "/v1/accounts", nil, &response); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if *jsonOutput {
			writeJSON(stdout, response)
			return 0
		}
		writeAccounts(stdout, response)
		return 0
	case "discover":
		if len(args) < 2 {
			fmt.Fprintln(stderr, "usage: redline account discover <claude|codex> [--json]")
			return 1
		}
		flags := flag.NewFlagSet("account discover", flag.ContinueOnError)
		flags.SetOutput(stderr)
		jsonOutput := flags.Bool("json", false, "emit JSON")
		if err := flags.Parse(args[2:]); err != nil {
			return 1
		}
		var response struct {
			Source string `json:"source"`
			Error  string `json:"error"`
			Hint   string `json:"hint"`
			Cards  []struct {
				ID            string `json:"id"`
				Plan          string `json:"plan"`
				Stale         bool   `json:"stale"`
				HasWeekly     bool   `json:"has_weekly"`
				Error         string `json:"error"`
				LinkedAccount string `json:"linked_account"`
			} `json:"cards"`
		}
		if err := client.Do(ctx, http.MethodGet, "/v1/accounts/discover/"+url.PathEscape(args[1]), nil, &response); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if *jsonOutput {
			writeJSON(stdout, response)
			return 0
		}
		if response.Error != "" {
			fmt.Fprintln(stdout, response.Error)
			return 1
		}
		if len(response.Cards) == 0 {
			fmt.Fprintf(stdout, "OpenUsage at %s has no %s cards.\n%s\n", response.Source, args[1], response.Hint)
			return 0
		}
		table := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(table, "CARD\tPLAN\tDATA\tLINKED TO")
		for _, card := range response.Cards {
			data := "ok"
			switch {
			case card.Error != "":
				data = card.Error
			case card.Stale:
				data = "stale"
			case !card.HasWeekly:
				data = "weekly not reported"
			}
			linked := card.LinkedAccount
			if linked == "" {
				linked = "-"
			}
			fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", card.ID, dash(card.Plan), data, linked)
		}
		_ = table.Flush()
		fmt.Fprintln(stdout, "\nLink a card with: redline account add <id> --provider "+args[1]+" --card <CARD> --label <name>")
		return 0
	case "add", "edit":
		if len(args) < 2 || strings.HasPrefix(args[1], "-") {
			fmt.Fprintf(stderr, "usage: redline account %s <id> [flags]\n", args[0])
			return 1
		}
		id := args[1]
		flags := flag.NewFlagSet("account "+args[0], flag.ContinueOnError)
		flags.SetOutput(stderr)
		provider := flags.String("provider", "", "provider type: claude or codex (add only)")
		label := flags.String("label", "", "display name, e.g. Personal or Client (no email addresses)")
		card := flags.String("card", "", "OpenUsage card id, e.g. claude@ab12cd34 (see `redline account discover`)")
		openUsageURL := flags.String("openusage-url", "", "OpenUsage local API URL")
		source := flags.String("source", "", "usage source: openusage, auto or native")
		jsonOutput := flags.Bool("json", false, "emit JSON")
		if err := flags.Parse(args[2:]); err != nil {
			return 1
		}
		set := map[string]bool{}
		flags.Visit(func(f *flag.Flag) { set[f.Name] = true })
		body := map[string]any{}
		if args[0] == "add" {
			if *provider == "" {
				fmt.Fprintln(stderr, "--provider is required")
				return 1
			}
			body["id"], body["provider"] = id, *provider
		} else if set["provider"] {
			fmt.Fprintln(stderr, "an account's provider cannot be changed; add a new account instead")
			return 1
		}
		for name, value := range map[string]*string{"label": label, "openusage_card": card, "openusage_url": openUsageURL, "usage_source": source} {
			flagName := map[string]string{"label": "label", "openusage_card": "card", "openusage_url": "openusage-url", "usage_source": "source"}[name]
			if set[flagName] {
				body[name] = *value
			}
		}
		if *card != "" && !set["source"] && args[0] == "add" {
			body["usage_source"] = "openusage"
		}
		method, path := http.MethodPost, "/v1/accounts"
		if args[0] == "edit" {
			method, path = http.MethodPatch, "/v1/accounts/"+url.PathEscape(id)
		}
		return accountChange(client, method, path, body, id, args[0], *jsonOutput, stdout, stderr)
	case "disable", "enable":
		if len(args) < 2 {
			fmt.Fprintf(stderr, "usage: redline account %s <id>\n", args[0])
			return 1
		}
		return accountChange(client, http.MethodPatch, "/v1/accounts/"+url.PathEscape(args[1]),
			map[string]any{"disabled": args[0] == "disable"}, args[1], args[0], false, stdout, stderr)
	case "remove":
		if len(args) < 2 {
			fmt.Fprintln(stderr, "usage: redline account remove <id>")
			return 1
		}
		return accountChange(client, http.MethodDelete, "/v1/accounts/"+url.PathEscape(args[1]), nil, args[1], "remove", false, stdout, stderr)
	default:
		fmt.Fprintln(stderr, accountUsage)
		return 1
	}
}

func accountChange(client apiclient.Client, method, path string, body any, id, verb string, jsonOutput bool, stdout, stderr io.Writer) int {
	var response cliAccounts
	if err := client.Do(context.Background(), method, path, body, &response); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if jsonOutput {
		writeJSON(stdout, response)
		return 0
	}
	past := map[string]string{"add": "added", "edit": "updated", "disable": "disabled", "enable": "enabled", "remove": "removed"}[verb]
	fmt.Fprintf(stdout, "%s account %s\n", past, id)
	for _, account := range response.Accounts {
		if account.ID == id && verb == "add" {
			fmt.Fprintln(stdout, "\n"+account.SignIn)
		}
	}
	if response.RestartRequired {
		fmt.Fprintln(stdout, "\nRestart the Redline service to apply account changes.")
	}
	return 0
}

func writeAccounts(output io.Writer, response cliAccounts) {
	table := tabwriter.NewWriter(output, 0, 2, 2, ' ', 0)
	fmt.Fprintln(table, "ID\tPROVIDER\tLABEL\tREADS\tFROM\tSTATE")
	for _, account := range response.Accounts {
		reads := account.OpenUsageCard
		if account.ClaudeDir != "" {
			reads = "profile " + account.ClaudeDir
		}
		if reads == "" {
			reads = "default login (" + account.UsageSource + ")"
		}
		state := account.Status
		switch {
		case account.Pending != "":
			state = "pending " + account.Pending + " (restart)"
		case account.Disabled:
			state = "disabled"
		case state == "":
			state = "-"
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\n", account.ID, account.Provider, dash(account.Label), reads, account.Origin, state)
	}
	_ = table.Flush()
	for _, account := range response.Accounts {
		if account.Status == "signed_out" || account.Status == "error" {
			fmt.Fprintf(output, "\n%s: %s\n  %s\n", account.ID, dash(account.LastError), account.SignIn)
		}
	}
	if response.RestartRequired {
		fmt.Fprintln(output, "\nSaved changes are waiting for a Redline service restart.")
	}
}

func dash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}
