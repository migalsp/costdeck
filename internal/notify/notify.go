// Package notify sends Cost Deck's messages (scaling changes, alerts and digests) to every
// configured channel: the Webex space, Slack, Microsoft Teams and email.
package notify

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/config"
	"github.com/migalsp/costdeck-operator/internal/finops"
	"github.com/migalsp/costdeck-operator/internal/netguard"
	"github.com/migalsp/costdeck-operator/internal/webex"
)

const (
	// EventSchedules is a schedule that finished scaling up or down.
	EventSchedules finopsv1.NotificationEvent = "schedules"
	// EventAlerts is a budget, a cost anomaly or a namespace about to be deleted.
	EventAlerts finopsv1.NotificationEvent = "alerts"
	// EventDigests is the scheduled cost summary.
	EventDigests finopsv1.NotificationEvent = "digests"
)

const (
	// SecretKeyWebhookURL holds a Slack or Teams webhook URL.
	SecretKeyWebhookURL = "WEBHOOK_URL"
	// SecretKeyPassword holds the SMTP password.
	SecretKeyPassword = "PASSWORD"
)

// Channel names, as reports and alerts say where they were sent.
const (
	ChannelWebex = "Webex"
	ChannelSlack = "Slack"
	ChannelTeams = "Teams"
	ChannelEmail = "email"
)

// Dispatcher sends a message to every channel that wants its event.
type Dispatcher struct {
	Client client.Reader
	// Webex posts to the Webex space; nil leaves Webex out.
	Webex *webex.Notifier
	// HTTP sends the webhooks. Defaults to a client that refuses metadata addresses.
	HTTP *http.Client
	// Mail sends email; tests replace it. Defaults to SendMail.
	Mail func(ctx context.Context, m Mail) error
}

// Send delivers markdown to the channels that want the event and returns their names. It
// fails only when every channel it tried failed: a channel that took the message must not
// get it again because another one was down, so partial failures are logged.
func (d *Dispatcher) Send(ctx context.Context, event finopsv1.NotificationEvent, markdown string) ([]string, error) {
	cfg, err := config.Get(ctx, d.Client)
	if err != nil {
		return nil, err
	}
	log := logf.FromContext(ctx).WithName("notify")
	var delivered []string
	var failures []error
	try := func(name string, send func() error) {
		if err := send(); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", name, err))
			return
		}
		delivered = append(delivered, name)
	}

	if d.Webex != nil && webexWants(cfg, event) {
		if err := d.Webex.Post(ctx, markdown); err == nil {
			delivered = append(delivered, ChannelWebex)
		} else if !errors.Is(err, webex.ErrNoSpace) {
			failures = append(failures, fmt.Errorf("%s: %w", ChannelWebex, err))
		}
	}
	titled := markdown
	if cfg.Spec.ClusterName != "" {
		titled = "**[" + cfg.Spec.ClusterName + "]** " + markdown
	}
	n := cfg.Spec.Integrations.Notifications
	if n == nil {
		n = &finopsv1.NotificationsConfig{}
	}
	if c := n.Slack; c != nil && c.Enabled && Wants(c.Events, event) {
		try(ChannelSlack, func() error {
			url, err := d.secret(ctx, c.SecretRef, SecretKeyWebhookURL)
			if err != nil {
				return err
			}
			return SendSlack(ctx, d.HTTPClient(), url, titled)
		})
	}
	if c := n.Teams; c != nil && c.Enabled && Wants(c.Events, event) {
		try(ChannelTeams, func() error {
			url, err := d.secret(ctx, c.SecretRef, SecretKeyWebhookURL)
			if err != nil {
				return err
			}
			return SendTeams(ctx, d.HTTPClient(), url, titled)
		})
	}
	if c := n.Email; c != nil && c.Enabled && Wants(c.Events, event) {
		try(ChannelEmail, func() error {
			m, err := d.mail(ctx, c, cfg.Spec.ClusterName, markdown)
			if err != nil {
				return err
			}
			if d.Mail != nil {
				return d.Mail(ctx, m)
			}
			return SendMail(ctx, m)
		})
	}

	if len(failures) > 0 && len(delivered) > 0 {
		log.Error(errors.Join(failures...), "Could not send a notification to every channel", "event", event, "delivered", delivered)
		return delivered, nil
	}
	return delivered, errors.Join(failures...)
}

// Notify sends a scaling change; failures are logged, since a notification must never
// hold up scaling.
func (d *Dispatcher) Notify(ctx context.Context, markdown string) {
	if _, err := d.Send(ctx, EventSchedules, markdown); err != nil {
		logf.FromContext(ctx).WithName("notify").Error(err, "Could not send a scaling notification")
	}
}

// Wants reports whether a channel limited to events receives event; no limit takes all.
func Wants(events []finopsv1.NotificationEvent, event finopsv1.NotificationEvent) bool {
	return len(events) == 0 || slices.Contains(events, event)
}

// webexWants keeps Webex as it was: scaling changes only with notifyTransitions, alerts
// and digests whenever the space is set.
func webexWants(cfg *finopsv1.CostDeckConfig, event finopsv1.NotificationEvent) bool {
	m := cfg.Spec.Integrations.Messenger
	if m == nil || m.Webex == nil || !m.Webex.Enabled {
		return false
	}
	return event != EventSchedules || m.Webex.NotifyTransitions
}

// HTTPClient is the client that sends webhooks.
func (d *Dispatcher) HTTPClient() *http.Client {
	if d.HTTP != nil {
		return d.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second, Transport: netguard.Transport()}
}

func (d *Dispatcher) secret(ctx context.Context, name, key string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("no Secret holds the %s", strings.ToLower(strings.ReplaceAll(key, "_", " ")))
	}
	var s corev1.Secret
	if err := d.Client.Get(ctx, client.ObjectKey{Namespace: config.OperatorNamespace(), Name: name}, &s); err != nil {
		return "", fmt.Errorf("read Secret %s: %w", name, err)
	}
	v := strings.TrimSpace(string(s.Data[key]))
	if v == "" {
		return "", fmt.Errorf("the Secret %s has no %s", name, key)
	}
	return v, nil
}

func (d *Dispatcher) mail(ctx context.Context, c *finopsv1.EmailChannelConfig, cluster, markdown string) (Mail, error) {
	m := Mail{Host: c.Host, Port: int(c.Port), Security: c.Security, Username: c.Username, From: c.From, To: c.To}
	if c.Username != "" {
		pw, err := d.secret(ctx, c.SecretRef, SecretKeyPassword)
		if err != nil {
			return m, err
		}
		m.Password = pw
	}
	m.Subject, m.Body = Email(cluster, markdown)
	return m, nil
}

// Poster sends one event's messages for finops: cost digests or alerts. It says where a
// message went, and wraps finops.ErrNoDestination when no channel takes the event.
type Poster struct {
	Dispatcher *Dispatcher
	Event      finopsv1.NotificationEvent
}

// Post sends markdown and returns the channels that took it, such as "Webex, Slack".
func (p Poster) Post(ctx context.Context, markdown string) (string, error) {
	delivered, err := p.Dispatcher.Send(ctx, p.Event, markdown)
	if err != nil {
		return "", err
	}
	if len(delivered) == 0 {
		return "", fmt.Errorf("%w: set up Webex, Slack, Teams or email under Settings → Notifications", finops.ErrNoDestination)
	}
	return strings.Join(delivered, ", "), nil
}

// Ready lists the channels set up to receive event, without sending anything: what the
// dashboard names next to "Send now" and above alerts.
func Ready(cfg *finopsv1.CostDeckConfig, event finopsv1.NotificationEvent) []string {
	out := []string{}
	if webexWants(cfg, event) && cfg.Spec.Integrations.Messenger.Webex.RoomID != "" {
		out = append(out, ChannelWebex)
	}
	n := cfg.Spec.Integrations.Notifications
	if n == nil {
		return out
	}
	hook := func(name string, c *finopsv1.WebhookChannelConfig) {
		if c != nil && c.Enabled && c.SecretRef != "" && Wants(c.Events, event) {
			out = append(out, name)
		}
	}
	hook(ChannelSlack, n.Slack)
	hook(ChannelTeams, n.Teams)
	if e := n.Email; e != nil && e.Enabled && e.Host != "" && len(e.To) > 0 && Wants(e.Events, event) {
		out = append(out, ChannelEmail)
	}
	return out
}
