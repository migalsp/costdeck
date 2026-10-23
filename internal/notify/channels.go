package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/migalsp/costdeck-operator/internal/netguard"
)

var (
	mdBold = regexp.MustCompile(`\*\*(.+?)\*\*`)
	mdLink = regexp.MustCompile(`\[([^\]]+)\]\((https?://[^)\s]+)\)`)
	mdHead = regexp.MustCompile(`(?m)^#{1,6}\s+(.+)$`)
)

// SendSlack posts markdown through a Slack incoming webhook.
func SendSlack(ctx context.Context, hc *http.Client, webhook, markdown string) error {
	return postJSON(ctx, hc, webhook, map[string]any{"text": SlackText(markdown)})
}

// SlackText rewrites markdown in Slack's mrkdwn: single asterisks for bold, <url|text>
// links, and &, < and > escaped.
func SlackText(markdown string) string {
	s := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(markdown)
	s = mdLink.ReplaceAllString(s, "<$2|$1>")
	s = mdHead.ReplaceAllString(s, "**$1**")
	return mdBold.ReplaceAllString(s, "*$1*")
}

// SendTeams posts markdown through a Microsoft Teams Workflows webhook as an Adaptive Card.
func SendTeams(ctx context.Context, hc *http.Client, webhook, markdown string) error {
	return postJSON(ctx, hc, webhook, TeamsCard(markdown))
}

// TeamsCard wraps markdown in an Adaptive Card, a text block per line: Teams joins the
// lines of one block into a paragraph.
func TeamsCard(markdown string) map[string]any {
	var body []any
	for line := range strings.SplitSeq(mdHead.ReplaceAllString(markdown, "**$1**"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		body = append(body, map[string]any{"type": "TextBlock", "text": line, "wrap": true, "spacing": "Small"})
	}
	return map[string]any{
		"type": "message",
		"attachments": []any{map[string]any{
			"contentType": "application/vnd.microsoft.card.adaptive",
			"content": map[string]any{
				"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
				"type":    "AdaptiveCard",
				"version": "1.4",
				"body":    body,
			},
		}},
	}
}

// Email turns markdown into a subject (its first line) and a plain-text body.
func Email(cluster, markdown string) (subject, body string) {
	plain := mdHead.ReplaceAllString(markdown, "$1")
	plain = mdLink.ReplaceAllString(plain, "$1 ($2)")
	plain = mdBold.ReplaceAllString(plain, "$1")
	first, _, _ := strings.Cut(strings.TrimSpace(plain), "\n")
	first = strings.TrimSpace(strings.TrimLeft(first, "-* "))
	if r := []rune(first); len(r) > 120 {
		first = string(r[:117]) + "…"
	}
	subject = "Cost Deck: " + first
	if cluster != "" {
		subject = "Cost Deck [" + cluster + "]: " + first
	}
	return subject, strings.TrimSpace(plain) + "\n\n-- \nSent by Cost Deck\n"
}

// CheckWebhook validates a webhook URL before it is stored: http or https with a host
// that is not a metadata address.
func CheckWebhook(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("the webhook must be an http or https URL")
	}
	if netguard.ForbiddenHost(u.Hostname()) {
		return netguard.ErrForbiddenAddress
	}
	return nil
}

func postJSON(ctx context.Context, hc *http.Client, target string, payload any) error {
	if err := CheckWebhook(target); err != nil {
		return err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := hc.Do(req)
	if err != nil {
		// The URL is a credential: report the host only.
		return fmt.Errorf("post to %s: %w", req.URL.Host, unwrapURL(err))
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode/100 != 2 {
		detail, _ := io.ReadAll(io.LimitReader(res.Body, 300))
		return fmt.Errorf("%s answered %s: %s", req.URL.Host, res.Status, strings.TrimSpace(string(detail)))
	}
	return nil
}

// unwrapURL drops the *url.Error wrapper, whose message repeats the whole URL.
func unwrapURL(err error) error {
	if ue, ok := err.(*url.Error); ok {
		return ue.Err
	}
	return err
}
