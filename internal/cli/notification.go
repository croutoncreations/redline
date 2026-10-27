package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/croutoncreations/redline/internal/apiclient"
	"github.com/croutoncreations/redline/internal/domain"
	"github.com/croutoncreations/redline/internal/notification"
)

func runNotificationTest(client apiclient.Client, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("notification test", flag.ContinueOnError)
	flags.SetOutput(stderr)
	event := flags.String("event", domain.EventResetExpiring, "event type, or all")
	provider := flags.String("provider", "", "provider account ID")
	jsonOutput := flags.Bool("json", false, "print delivery JSON")
	pushTest := flags.Bool("push", false, "await a content-free test wake")
	if err := flags.Parse(args); err != nil {
		return 1
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: redline notification test [--event TYPE|all] [--provider ID] [--push] [--json]")
		return 1
	}
	events := []string{*event}
	if *event == "all" {
		events = notification.EventTypes()
	}
	deliveries := make([]domain.NotificationDelivery, 0, len(events))
	exit := 0
	for i, kind := range events {
		if i > 0 {
			time.Sleep(time.Second)
		}
		var delivery domain.NotificationDelivery
		body := map[string]any{"event": kind, "provider": *provider}
		if *pushTest {
			body["push"] = true
		}
		if err := client.Do(context.Background(), http.MethodPost, "/v1/notifications/test", body, &delivery); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		deliveries = append(deliveries, delivery)
		if delivery.Status != "delivered" {
			exit = 1
		}
		if !*jsonOutput {
			var payload domain.NotificationEvent
			_ = json.Unmarshal(delivery.Payload, &payload)
			fmt.Fprintf(stdout, "%s\n%s\nDelivery %d: %s", payload.Title, payload.Message, delivery.ID, delivery.Status)
			if delivery.LastError != "" {
				fmt.Fprintf(stdout, " (%s)", delivery.LastError)
			}
			fmt.Fprintln(stdout)
			if *pushTest && delivery.Push != nil {
				if !delivery.Push.Configured {
					fmt.Fprintln(stdout, "push not configured")
				} else {
					fmt.Fprintf(stdout, "Push: %d devices woken\n", delivery.Push.Woken)
				}
			}
		}
	}
	if *jsonOutput {
		if *event == "all" {
			writeJSON(stdout, deliveries)
		} else {
			writeJSON(stdout, deliveries[0])
		}
	}
	return exit
}
