package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"Zeus/alerting"
	"Zeus/server"
	"Zeus/storage"
)

// Alerting flags. Kept in their own file so main.go stays readable.
var (
	flagAlerts             = flag.String("alerts", "", "Path to a JSON alert rules file (built-in defaults are used when unset)")
	flagWriteDefaultAlerts = flag.String("write-default-alerts", "", "Write the default alert rules to this path and exit")
	flagAlertWebhook       = flag.String("alert-webhook", "", "POST alerts as JSON to this URL")
	flagAlertSlack         = flag.String("alert-slack", "", "Post alerts to this Slack-compatible incoming webhook URL")
	flagNoAlerts           = flag.Bool("no-alerts", false, "Disable alert evaluation")
)

// maybeWriteDefaultAlerts handles -write-default-alerts and reports whether the
// process should exit.
func maybeWriteDefaultAlerts() bool {
	if *flagWriteDefaultAlerts == "" {
		return false
	}
	if err := alerting.WriteDefaultRules(*flagWriteDefaultAlerts); err != nil {
		log.Fatalf("write default alerts: %v", err)
	}
	fmt.Printf("Default alert rules written to %s\n", *flagWriteDefaultAlerts)
	fmt.Printf("Edit it, then run with: wmonitor -alerts %s\n", *flagWriteDefaultAlerts)
	return true
}

// buildEvaluator constructs the alert evaluator and attaches it to the server.
//
// Returns nil when alerting is disabled, in which case /api/alerts is not
// registered at all. That is deliberate: an endpoint returning an empty list
// reads as "nothing is wrong", which is a dangerous thing to say when nothing is
// actually being evaluated.
func buildEvaluator(store storage.Store, srv *server.Server) *alerting.Evaluator {
	if *flagNoAlerts {
		log.Println("[alerting] disabled by -no-alerts")
		return nil
	}

	rules, err := alerting.LoadRules(*flagAlerts)
	if err != nil {
		// Abort rather than fall back to defaults. Silently ignoring a broken rules
		// file would leave the operator believing their custom thresholds are
		// active when they are not.
		log.Fatalf("alert rules: %v", err)
	}
	if *flagAlerts == "" {
		log.Printf("[alerting] using %d built-in default rule(s); customise with -write-default-alerts", len(rules))
	}

	var notifiers []alerting.Notifier
	if *flagAlertWebhook != "" {
		notifiers = append(notifiers, alerting.NewWebhookNotifier(*flagAlertWebhook))
		log.Println("[alerting] webhook notifier enabled")
	}
	if *flagAlertSlack != "" {
		notifiers = append(notifiers, alerting.NewSlackNotifier(*flagAlertSlack))
		log.Println("[alerting] slack notifier enabled")
	}
	if len(notifiers) == 0 {
		log.Println("[alerting] no external notifier configured — alerts will only appear in the log and at /api/alerts. Set -alert-webhook or -alert-slack to be told about problems.")
	}

	ev := alerting.New(store, rules, notifiers...)
	if srv != nil {
		srv.SetAlertSource(ev)
	}
	return ev
}

// alertWebhookFromEnv lets deployments configure sinks without editing the
// service command line (Render, systemd unit files, and so on).
func alertEnvDefaults() {
	if *flagAlertWebhook == "" {
		*flagAlertWebhook = os.Getenv("WMONITOR_ALERT_WEBHOOK")
	}
	if *flagAlertSlack == "" {
		*flagAlertSlack = os.Getenv("WMONITOR_ALERT_SLACK")
	}
	if *flagAlerts == "" {
		*flagAlerts = os.Getenv("WMONITOR_ALERT_RULES")
	}
}

// runEvaluator starts the evaluation loop if alerting is enabled.
func runEvaluator(ctx context.Context, ev *alerting.Evaluator) {
	if ev == nil {
		return
	}
	go ev.Run(ctx)
}
