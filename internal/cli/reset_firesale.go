package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"

	"github.com/croutoncreations/redline/internal/apiclient"
	"github.com/croutoncreations/redline/internal/resetsignal"
)

func runResetFiresale(client apiclient.Client, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "start" && args[0] != "stop") {
		fmt.Fprintln(stderr, "usage: redline reset firesale start|stop [--announcement KEY]")
		return 1
	}
	flags := flag.NewFlagSet("reset firesale "+args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	key := flags.String("announcement", "", "announcement key (defaults to current announcement for start, active sale for stop)")
	if err := flags.Parse(args[1:]); err != nil {
		return 1
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "unexpected arguments")
		return 1
	}
	if *key == "" {
		var state resetsignal.State
		if err := client.Do(context.Background(), http.MethodGet, "/v1/reset-signals", nil, &state); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if args[0] == "stop" {
			*key = state.Firesale.AnnouncementKey
		} else {
			if state.Announcement == nil {
				fmt.Fprintln(stderr, "no current reset announcement")
				return 1
			}
			*key = state.Announcement.Key
		}
	}
	body := map[string]string{"announcement_key": *key, "action": args[0]}
	var state resetsignal.FiresaleState
	if err := client.Do(context.Background(), http.MethodPost, "/v1/reset-signals/firesale", body, &state); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	writeJSON(stdout, state)
	return 0
}
