package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/croutoncreations/redline/internal/apiclient"
)

func runIssueSources(client apiclient.Client, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: redline issue-source <add --file definition.yaml|list|preview ID|sync ID|enable ID|disable ID>")
		return 1
	}
	method, path := http.MethodGet, "/v1/issue-sources"
	var body any
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return 1
		}
	case "add":
		flags := flag.NewFlagSet("issue-source add", flag.ContinueOnError)
		flags.SetOutput(stderr)
		file := flags.String("file", "", "YAML source definition")
		if err := flags.Parse(args[1:]); err != nil {
			return 1
		}
		if *file == "" || len(flags.Args()) > 0 {
			fmt.Fprintln(stderr, "--file is required")
			return 1
		}
		definition, err := readYAML(*file)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		body = definition
		method = http.MethodPost
	case "preview", "sync", "enable", "disable":
		if len(args) != 2 {
			fmt.Fprintln(stderr, "source ID is required")
			return 1
		}
		path += "/" + url.PathEscape(args[1]) + "/" + args[0]
		if args[0] != "preview" {
			method = http.MethodPost
		}
	default:
		fmt.Fprintln(stderr, "unknown issue-source command")
		return 1
	}
	var out any
	if err := client.Do(context.Background(), method, path, body, &out); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	writeJSON(stdout, out)
	return 0
}
func runIssues(client apiclient.Client, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: redline issue <list|retry ITEM_ID|retry-delivery TASK_ID>")
		return 1
	}
	method, path := http.MethodGet, "/v1/issues"
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return 1
		}
	case "retry", "retry-delivery":
		if len(args) != 2 {
			fmt.Fprintln(stderr, "item or task ID is required")
			return 1
		}
		method = http.MethodPost
		if args[0] == "retry" {
			path += "/" + url.PathEscape(args[1]) + "/retry"
		} else {
			path = "/v1/issue-delivery/" + url.PathEscape(args[1]) + "/retry"
		}
	default:
		fmt.Fprintln(stderr, "unknown issue command")
		return 1
	}
	var out any
	if err := client.Do(context.Background(), method, path, nil, &out); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	writeJSON(stdout, out)
	return 0
}
