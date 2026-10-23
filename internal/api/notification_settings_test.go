package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/migalsp/costdeck-operator/internal/config"
)

func TestNotificationSettings(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServerWithK8s()
	ctx := context.Background()
	hook := "https://hooks.slack.com/services/T000/B000/s3cr3tpath"

	for body, why := range map[string]string{
		`{"integrations":{"notifications":{"slack":{"enabled":true}}}}`:                                                                                           "Slack without a webhook",
		`{"integrations":{"notifications":{"slack":{"webhookUrl":"ftp://example.com/x"}}}}`:                                                                       "an ftp webhook",
		`{"integrations":{"notifications":{"teams":{"webhookUrl":"http://169.254.169.254/latest"}}}}`:                                                             "a metadata webhook",
		`{"integrations":{"notifications":{"slack":{"events":["everything"]}}}}`:                                                                                  "an unknown event",
		`{"integrations":{"notifications":{"slack":{"events":[]}}}}`:                                                                                              "no event at all",
		`{"integrations":{"notifications":{"email":{"enabled":true,"host":"smtp.example.com","from":"a@example.com"}}}}`:                                          "email without recipients",
		`{"integrations":{"notifications":{"email":{"enabled":true,"host":"h","from":"a@example.com","to":["not an address"]}}}}`:                                 "a bad recipient",
		`{"integrations":{"notifications":{"email":{"enabled":true,"host":"h","from":"a@example.com","to":["b@example.com"],"username":"u","security":"none"}}}}`: "a password without TLS",
	} {
		if rr := send(t, server, "PUT", "/api/settings", body); rr.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", why, rr.Code, rr.Body.String())
		}
	}

	ok := `{"integrations":{"notifications":{
		"slack":{"enabled":true,"webhookUrl":"` + hook + `","events":["alerts","digests"]},
		"teams":{"enabled":false},
		"email":{"enabled":true,"host":"smtp.example.com","port":587,"from":"Cost Deck <costdeck@example.com>","to":["finops@example.com"," finops@example.com","ops@example.com"],
			"username":"costdeck","password":"smtp-pass","events":["schedules","alerts","digests"]}}}}`
	if rr := send(t, server, "PUT", "/api/settings", ok); rr.Code != http.StatusOK {
		t.Fatalf("valid settings: %d %s", rr.Code, rr.Body.String())
	}
	cfg, _ := config.Get(ctx, server.Client)
	n := cfg.Spec.Integrations.Notifications
	if n == nil || n.Slack == nil || !n.Slack.Enabled || n.Slack.SecretRef != slackSecret || len(n.Slack.Events) != 2 {
		t.Fatalf("slack = %+v", n)
	}
	if e := n.Email; e == nil || e.SecretRef != smtpSecret || strings.Join(e.To, ",") != "finops@example.com,ops@example.com" || e.Events != nil {
		t.Errorf("email = %+v: duplicates stay, or all three events are not stored as none", e)
	}
	var secret corev1.Secret
	if err := server.Client.Get(ctx, client.ObjectKey{Namespace: "costdeck", Name: slackSecret}, &secret); err != nil || string(secret.Data["WEBHOOK_URL"]) != hook {
		t.Errorf("slack secret = %v (%v)", secret.Data, err)
	}

	rr := send(t, server, "GET", "/api/settings", "")
	if body := rr.Body.String(); strings.Contains(body, "s3cr3tpath") || strings.Contains(body, "smtp-pass") {
		t.Error("a webhook URL or the SMTP password came back")
	}
	var got struct {
		Integrations struct {
			Notifications NotificationsSettingsResponse `json:"notifications"`
		} `json:"integrations"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	g := got.Integrations.Notifications
	if !g.Slack.HasWebhook || g.Teams.HasWebhook || !g.Email.HasPassword || g.Email.Security != securityStartTLS || len(g.Email.Events) != 0 {
		t.Errorf("response = %+v", g)
	}

	// Switching a channel off keeps its webhook, so switching it on again needs nothing more.
	if rr := send(t, server, "PUT", "/api/settings", `{"integrations":{"notifications":{"slack":{"enabled":false}}}}`); rr.Code != http.StatusOK {
		t.Fatal(rr.Body.String())
	}
	if rr := send(t, server, "PUT", "/api/settings", `{"integrations":{"notifications":{"slack":{"enabled":true}}}}`); rr.Code != http.StatusOK {
		t.Errorf("switching Slack on again: %d %s", rr.Code, rr.Body.String())
	}
}

func TestNotificationTestMessages(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServerWithK8s()
	var mu sync.Mutex
	var bodies []string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(raw))
		mu.Unlock()
	}))
	defer hook.Close()
	server.HTTPClient = hook.Client()

	// An unsaved URL from the form is used for the test.
	rr := send(t, server, "POST", "/api/settings/providers/slack/test", `{"slack":{"webhookUrl":"`+hook.URL+`"}}`)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"connected":true`) {
		t.Fatalf("slack test: %d %s", rr.Code, rr.Body.String())
	}
	// The stored one otherwise.
	if rr := send(t, server, "PUT", "/api/settings", `{"integrations":{"notifications":{"teams":{"enabled":true,"webhookUrl":"`+hook.URL+`"}}}}`); rr.Code != http.StatusOK {
		t.Fatal(rr.Body.String())
	}
	if rr := send(t, server, "POST", "/api/settings/providers/teams/test", `{}`); !strings.Contains(rr.Body.String(), `"connected":true`) {
		t.Fatalf("teams test: %s", rr.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 || !strings.Contains(bodies[0], `"text":"*Cost Deck test message*`) || !strings.Contains(bodies[1], `AdaptiveCard`) {
		t.Errorf("received %v", bodies)
	}
	if rr := send(t, server, "POST", "/api/settings/providers/slack/test", `{}`); !strings.Contains(rr.Body.String(), "enter the webhook URL first") {
		t.Errorf("a test with no URL: %s", rr.Body.String())
	}
}
