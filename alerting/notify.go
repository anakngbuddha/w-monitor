package alerting

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

// Notifier delivers an alert somewhere a human will see it.
type Notifier interface {
	Send(a Alert) error
	Name() string
}

// LogNotifier writes alerts to the process log. Always registered, so an alert
// is never lost just because no external sink was configured.
type LogNotifier struct{}

func (LogNotifier) Name() string { return "log" }

func (LogNotifier) Send(a Alert) error {
	log.Printf("[alert] %s — %s", a.Title(), a.Message)
	return nil
}

// WebhookNotifier POSTs the alert as JSON to an arbitrary URL.
type WebhookNotifier struct {
	URL    string
	Client *http.Client
}

func NewWebhookNotifier(url string) *WebhookNotifier {
	return &WebhookNotifier{
		URL:    url,
		Client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (w *WebhookNotifier) Name() string { return "webhook" }

func (w *WebhookNotifier) Send(a Alert) error {
	body, err := json.Marshal(a)
	if err != nil {
		return err
	}
	resp, err := w.Client.Post(w.URL, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("webhook: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned HTTP %d", resp.StatusCode)
	}
	return nil
}

// SlackNotifier posts to a Slack-compatible incoming webhook.
type SlackNotifier struct {
	URL    string
	Client *http.Client
}

func NewSlackNotifier(url string) *SlackNotifier {
	return &SlackNotifier{
		URL:    url,
		Client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (s *SlackNotifier) Name() string { return "slack" }

func (s *SlackNotifier) Send(a Alert) error {
	emoji := ":warning:"
	if a.Resolved {
		emoji = ":white_check_mark:"
	} else if a.Severity == SeverityCritical {
		emoji = ":rotating_light:"
	}

	payload := map[string]string{
		"text": fmt.Sprintf("%s *%s*\n%s", emoji, a.Title(), a.Message),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	resp, err := s.Client.Post(s.URL, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("slack: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("slack returned HTTP %d", resp.StatusCode)
	}
	return nil
}

// fanOut sends to every notifier, collecting failures.
//
// One broken sink must never suppress the others: if a Slack webhook has been
// revoked, the operator still needs the email and the log line.
func fanOut(notifiers []Notifier, a Alert) {
	for _, n := range notifiers {
		if err := n.Send(a); err != nil {
			log.Printf("[alert] notifier %q failed: %v", n.Name(), err)
		}
	}
}
