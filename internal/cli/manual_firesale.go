package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/croutoncreations/redline/internal/apiclient"
)

func runManualFiresale(client apiclient.Client, args []string, stdout, stderr io.Writer) int {
	usage := func() int {
		fmt.Fprintln(stderr, "usage: redline firesale <start --provider ACCOUNT (--for DURATION | --until-reset)|stop --provider ACCOUNT|status>")
		return 1
	}
	if len(args) == 0 {
		return usage()
	}
	flags := flag.NewFlagSet("firesale "+args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	provider := flags.String("provider", "", "provider account ID")
	var duration string
	var untilReset bool
	if args[0] == "start" {
		flags.StringVar(&duration, "for", "", "duration (5m to 168h)")
		flags.BoolVar(&untilReset, "until-reset", false, "until the weekly reset")
	}
	if err := flags.Parse(args[1:]); err != nil {
		return 1
	}
	if flags.NArg() != 0 {
		return usage()
	}
	method, path := http.MethodGet, "/v1/firesales"
	var body any
	var response any
	switch args[0] {
	case "start":
		if *provider == "" || (duration != "") == untilReset {
			return usage()
		}
		until := "weekly_reset"
		if !untilReset {
			d, err := time.ParseDuration(duration)
			if err != nil || d < 5*time.Minute || d > 7*24*time.Hour {
				return usage()
			}
			until = duration
		}
		method = http.MethodPost
		body = map[string]string{"account": *provider, "until": until}
	case "stop":
		if *provider == "" {
			return usage()
		}
		method = http.MethodDelete
		path += "/" + url.PathEscape(*provider)
	case "status":
		if *provider != "" {
			return usage()
		}
	default:
		return usage()
	}
	var output any = &response
	if method == http.MethodDelete {
		output = nil
	}
	if err := client.Do(context.Background(), method, path, body, output); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if output != nil {
		writeJSON(stdout, response)
	}
	return 0
}
