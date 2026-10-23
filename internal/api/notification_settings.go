package api

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/notify"
)

// Secrets holding the notification channels' credentials.
const (
	slackSecret = "costdeck-slack-webhook"
	teamsSecret = "costdeck-teams-webhook"
	smtpSecret  = "costdeck-smtp-credentials"
)

// Channels the test endpoint and the email security setting know.
const (
	channelSlack     = "slack"
	channelTeams     = "teams"
	channelEmail     = "email"
	securityStartTLS = "starttls"
)

var notificationEvents = []finopsv1.NotificationEvent{notify.EventSchedules, notify.EventAlerts, notify.EventDigests}

// NotificationsSettingsResponse describes Slack, Teams and email; the webhook URLs and the
// password are only reported as set or not.
type NotificationsSettingsResponse struct {
	Slack WebhookChannelResponse `json:"slack"`
	Teams WebhookChannelResponse `json:"teams"`
	Email EmailChannelResponse   `json:"email"`
}

type WebhookChannelResponse struct {
	Enabled    bool                         `json:"enabled"`
	HasWebhook bool                         `json:"hasWebhook"`
	Events     []finopsv1.NotificationEvent `json:"events"`
}

type EmailChannelResponse struct {
	Enabled     bool                         `json:"enabled"`
	Host        string                       `json:"host,omitempty"`
	Port        int32                        `json:"port,omitempty"`
	Security    string                       `json:"security"`
	From        string                       `json:"from,omitempty"`
	To          []string                     `json:"to"`
	Username    string                       `json:"username,omitempty"`
	HasPassword bool                         `json:"hasPassword"`
	Events      []finopsv1.NotificationEvent `json:"events"`
}

// NotificationsUpdateRequest changes the channels; a field left out stays as it is.
type NotificationsUpdateRequest struct {
	Slack *WebhookChannelUpdate `json:"slack,omitempty"`
	Teams *WebhookChannelUpdate `json:"teams,omitempty"`
	Email *EmailChannelUpdate   `json:"email,omitempty"`
}

type WebhookChannelUpdate struct {
	Enabled *bool `json:"enabled,omitempty"`
	// WebhookURL replaces the stored URL when set.
	WebhookURL string                        `json:"webhookUrl,omitempty"`
	Events     *[]finopsv1.NotificationEvent `json:"events,omitempty"`
}

type EmailChannelUpdate struct {
	Enabled  *bool     `json:"enabled,omitempty"`
	Host     *string   `json:"host,omitempty"`
	Port     *int32    `json:"port,omitempty"`
	Security *string   `json:"security,omitempty"`
	From     *string   `json:"from,omitempty"`
	To       *[]string `json:"to,omitempty"`
	Username *string   `json:"username,omitempty"`
	// Password replaces the stored password when set.
	Password string                        `json:"password,omitempty"`
	Events   *[]finopsv1.NotificationEvent `json:"events,omitempty"`
}

func notificationsResponse(n *finopsv1.NotificationsConfig) *NotificationsSettingsResponse {
	if n == nil {
		n = &finopsv1.NotificationsConfig{}
	}
	hook := func(c *finopsv1.WebhookChannelConfig) WebhookChannelResponse {
		if c == nil {
			return WebhookChannelResponse{Events: []finopsv1.NotificationEvent{}}
		}
		return WebhookChannelResponse{Enabled: c.Enabled, HasWebhook: c.SecretRef != "", Events: orEmpty(c.Events)}
	}
	out := &NotificationsSettingsResponse{Slack: hook(n.Slack), Teams: hook(n.Teams), Email: EmailChannelResponse{Security: securityStartTLS, To: []string{}, Events: []finopsv1.NotificationEvent{}}}
	if e := n.Email; e != nil {
		out.Email = EmailChannelResponse{Enabled: e.Enabled, Host: e.Host, Port: e.Port, Security: e.Security, From: e.From, To: e.To,
			Username: e.Username, HasPassword: e.SecretRef != "", Events: orEmpty(e.Events)}
		if out.Email.Security == "" {
			out.Email.Security = securityStartTLS
		}
		if out.Email.To == nil {
			out.Email.To = []string{}
		}
	}
	return out
}

func orEmpty(e []finopsv1.NotificationEvent) []finopsv1.NotificationEvent {
	if e == nil {
		return []finopsv1.NotificationEvent{}
	}
	return e
}

// checkEvents refuses unknown events; all three is the same as none, so it is stored as none.
func checkEvents(events []finopsv1.NotificationEvent) ([]finopsv1.NotificationEvent, error) {
	var out []finopsv1.NotificationEvent
	for _, e := range events {
		if !slices.Contains(notificationEvents, e) {
			return nil, badRequestf("unknown notification event %q: use schedules, alerts or digests", e)
		}
		if !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	if len(out) == len(notificationEvents) {
		return nil, nil
	}
	if events != nil && len(out) == 0 {
		return nil, badRequestf("pick at least one kind of message, or switch the channel off")
	}
	return out, nil
}

func (s *Server) applyNotificationSettings(ctx context.Context, cfg *finopsv1.CostDeckConfig, req *SettingsUpdateRequest) error {
	if req.Integrations == nil || req.Integrations.Notifications == nil {
		return nil
	}
	nr := req.Integrations.Notifications
	if cfg.Spec.Integrations.Notifications == nil {
		cfg.Spec.Integrations.Notifications = &finopsv1.NotificationsConfig{}
	}
	n := cfg.Spec.Integrations.Notifications
	hook := func(name, secret string, cur **finopsv1.WebhookChannelConfig, u *WebhookChannelUpdate) error {
		if u == nil {
			return nil
		}
		if *cur == nil {
			*cur = &finopsv1.WebhookChannelConfig{}
		}
		c := *cur
		if u.Enabled != nil {
			c.Enabled = *u.Enabled
		}
		if u.Events != nil {
			ev, err := checkEvents(*u.Events)
			if err != nil {
				return err
			}
			c.Events = ev
		}
		if u.WebhookURL != "" {
			if err := notify.CheckWebhook(u.WebhookURL); err != nil {
				return badRequestf("%s: %v", name, err)
			}
			if err := s.storeCredentials(ctx, secret, map[string][]byte{notify.SecretKeyWebhookURL: []byte(strings.TrimSpace(u.WebhookURL))}, &c.SecretRef); err != nil {
				return err
			}
		}
		if c.Enabled && c.SecretRef == "" {
			return badRequestf("%s needs its webhook URL", name)
		}
		return nil
	}
	if err := hook("Slack", slackSecret, &n.Slack, nr.Slack); err != nil {
		return err
	}
	if err := hook("Teams", teamsSecret, &n.Teams, nr.Teams); err != nil {
		return err
	}
	if u := nr.Email; u != nil {
		if n.Email == nil {
			n.Email = &finopsv1.EmailChannelConfig{}
		}
		e := n.Email
		if err := mergeEmail(e, u); err != nil {
			return err
		}
		if u.Password != "" {
			if err := s.storeCredentials(ctx, smtpSecret, map[string][]byte{notify.SecretKeyPassword: []byte(u.Password)}, &e.SecretRef); err != nil {
				return err
			}
		}
		if e.Enabled {
			if e.Host == "" || e.From == "" || len(e.To) == 0 {
				return badRequestf("email needs the SMTP server, a sender and at least one recipient")
			}
			if e.Username != "" && e.Security == "none" {
				return badRequestf("email: a password is only sent over TLS; choose starttls or tls, or leave the username empty")
			}
		}
	}
	return nil
}

// mergeEmail copies the fields a request sets onto the stored email channel.
func mergeEmail(e *finopsv1.EmailChannelConfig, u *EmailChannelUpdate) error {
	if u.Enabled != nil {
		e.Enabled = *u.Enabled
	}
	if u.Host != nil {
		e.Host = strings.TrimSpace(*u.Host)
	}
	if u.Port != nil {
		if *u.Port < 0 || *u.Port > 65535 {
			return badRequestf("email: the port must be between 1 and 65535")
		}
		e.Port = *u.Port
	}
	if u.Security != nil {
		switch *u.Security {
		case "", securityStartTLS, "tls", "none":
			e.Security = *u.Security
		default:
			return badRequestf("email: security is starttls, tls or none")
		}
	}
	if u.From != nil {
		e.From = strings.TrimSpace(*u.From)
	}
	if u.To != nil {
		var to []string
		for _, t := range *u.To {
			if t = strings.TrimSpace(t); t != "" && !slices.Contains(to, t) {
				to = append(to, t)
			}
		}
		e.To = to
	}
	if u.Username != nil {
		e.Username = strings.TrimSpace(*u.Username)
	}
	if u.Events != nil {
		ev, err := checkEvents(*u.Events)
		if err != nil {
			return err
		}
		e.Events = ev
	}
	if e.From != "" || len(e.To) > 0 {
		if err := notify.CheckAddresses(e.From, e.To); err != nil && e.Enabled {
			return badRequestf("email: %v", err)
		}
	}
	return nil
}

// testNotificationChannel sends a test message through one channel, with the unsaved form
// values first and the stored settings for the rest.
func (s *Server) testNotificationChannel(w http.ResponseWriter, r *http.Request, saved *finopsv1.CostDeckConfig, channel string) {
	var req NotificationsUpdateRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx := r.Context()
	n := saved.Spec.Integrations.Notifications
	if n == nil {
		n = &finopsv1.NotificationsConfig{}
	}
	text := "**Cost Deck test message**\nNotifications from " + clusterLabel(saved) + " reach this channel."
	var err error
	switch channel {
	case channelSlack, channelTeams:
		cur, u, send := n.Slack, req.Slack, notify.SendSlack
		if channel == channelTeams {
			cur, u, send = n.Teams, req.Teams, notify.SendTeams
		}
		url := ""
		if u != nil {
			url = strings.TrimSpace(u.WebhookURL)
		}
		if url == "" && cur != nil && cur.SecretRef != "" {
			url = s.secretValue(ctx, cur.SecretRef, notify.SecretKeyWebhookURL)
		}
		if url == "" {
			err = fmt.Errorf("enter the webhook URL first")
			break
		}
		err = send(ctx, s.webhookClient(), url, text)
	case channelEmail:
		e := finopsv1.EmailChannelConfig{}
		if n.Email != nil {
			e = *n.Email
		}
		password := ""
		if req.Email != nil {
			if err = mergeEmail(&e, req.Email); err != nil {
				break
			}
			password = req.Email.Password
		}
		if password == "" && e.SecretRef != "" {
			password = s.secretValue(ctx, e.SecretRef, notify.SecretKeyPassword)
		}
		m := notify.Mail{Host: e.Host, Port: int(e.Port), Security: e.Security, Username: e.Username, Password: password, From: e.From, To: e.To}
		m.Subject, m.Body = notify.Email(saved.Spec.ClusterName, text)
		err = notify.SendMail(ctx, m)
	}
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"connected": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"connected": true, "message": "Sent a test message"})
}

func clusterLabel(cfg *finopsv1.CostDeckConfig) string {
	if cfg.Spec.ClusterName != "" {
		return "cluster " + cfg.Spec.ClusterName
	}
	return "this cluster"
}

// secretValue reads one key of a Secret in the operator namespace, "" when missing.
func (s *Server) secretValue(ctx context.Context, name, key string) string {
	data, err := s.secretDataOrEmpty(ctx, name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data[key]))
}

// webhookClient sends test webhooks; tests replace Server.HTTPClient.
func (s *Server) webhookClient() *http.Client {
	if s.HTTPClient != nil {
		return s.HTTPClient
	}
	return (&notify.Dispatcher{}).HTTPClient()
}
