package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/croutoncreations/redline/internal/apiclient"
	"github.com/croutoncreations/redline/internal/store"
)

func runDevices(client apiclient.Client, args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && args[0] == "list" {
		var devices []store.DeviceSession
		if err := client.Do(context.Background(), http.MethodGet, "/v1/devices", nil, &devices); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		writeJSON(stdout, devices)
		return 0
	}
	if len(args) == 2 && args[0] == "revoke" && args[1] != "" {
		if err := client.Do(context.Background(), http.MethodDelete, "/v1/devices/"+url.PathEscape(args[1]), nil, nil); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintln(stdout, "Revoked device", args[1])
		return 0
	}
	fmt.Fprintln(stderr, "usage: redline devices <list|revoke ID>")
	return 1
}
