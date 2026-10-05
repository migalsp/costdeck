package scaling

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// restClient calls a cloud management REST API with a bearer token. Azure Resource
// Manager and the Google Cloud APIs are plain JSON over HTTPS, so talking to them directly
// keeps the binary free of two full cloud SDKs.
type restClient struct {
	http *http.Client
}

func newRESTClient(ctx context.Context, ts oauth2.TokenSource) *restClient {
	base := &http.Client{Timeout: 30 * time.Second}
	client := oauth2.NewClient(context.WithValue(ctx, oauth2.HTTPClient, base), oauth2.ReuseTokenSource(nil, ts))
	// The oauth2 transport adds the bearer token to every request it sends, redirects
	// included, so a redirect may never leave the host the request was made to.
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		if !sameOrigin(via[0].URL.String(), req.URL.String()) {
			return fmt.Errorf("refusing a redirect to %s", req.URL.Host)
		}
		return nil
	}
	return &restClient{http: client}
}

// sameOrigin reports whether link has base's scheme and host. Paging links come from the
// response and are followed with the cloud token, so they must stay on the API's host.
func sameOrigin(base, link string) bool {
	b, errB := url.Parse(base)
	l, errL := url.Parse(link)
	return errB == nil && errL == nil && l.User == nil && b.Scheme == l.Scheme && strings.EqualFold(b.Host, l.Host)
}

// nextLink returns a paging link to follow, or an error when it leaves the API's host.
func nextLink(base, link string) (string, error) {
	if link == "" || sameOrigin(base, link) {
		return link, nil
	}
	return "", fmt.Errorf("refusing a paging link outside %s", base)
}

// apiError is a non-2xx answer. Status lets callers treat "already in that state"
// conflicts as success.
type apiError struct {
	Status int
	Body   string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.Status, e.Body)
}

// do sends a request and decodes a JSON answer into out (when out is not nil).
func (c *restClient) do(ctx context.Context, method, endpoint string, body, out any) error {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &apiError{Status: resp.StatusCode, Body: errorText(data)}
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

// errorText pulls the message out of Azure ({"error":{"message"}}) and Google
// ({"error":{"message"}}) error bodies, falling back to the raw text.
func errorText(data []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &e) == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	text := strings.TrimSpace(string(data))
	if len(text) > 300 {
		text = text[:300] + "…"
	}
	return text
}

// Normalised resource states shown in discovery results.
const (
	stateRunning = "running"
	stateStopped = "stopped"
)

// matchLabels reports whether resource labels or tags carry every key/value of filter.
func matchLabels(labels, filter map[string]string) bool {
	for k, v := range filter {
		if labels[k] != v {
			return false
		}
	}
	return true
}
