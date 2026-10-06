package webex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is the Webex REST API root.
const DefaultBaseURL = "https://webexapis.com/v1"

// Client is a minimal Webex REST client for a bot token.
type Client struct {
	Token   string
	BaseURL string
	HTTP    *http.Client
}

// NewClient returns a client for the public Webex API.
func NewClient(token string) *Client {
	return &Client{Token: token, BaseURL: DefaultBaseURL, HTTP: &http.Client{Timeout: 15 * time.Second}}
}

// Person is the subset of /people fields the bot needs.
type Person struct {
	ID          string   `json:"id"`
	DisplayName string   `json:"displayName"`
	Emails      []string `json:"emails"`
}

// Room is the subset of /rooms fields the bot needs.
type Room struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Type is "direct" for 1:1 spaces and "group" for group spaces.
	Type string `json:"type"`
}

// Message is a Webex message.
type Message struct {
	ID          string    `json:"id"`
	ParentID    string    `json:"parentId,omitempty"`
	RoomID      string    `json:"roomId"`
	RoomType    string    `json:"roomType"`
	Text        string    `json:"text"`
	PersonID    string    `json:"personId"`
	PersonEmail string    `json:"personEmail"`
	Created     time.Time `json:"created"`
}

// APIError is a non-2xx answer from Webex.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	msg := e.Body
	var parsed struct {
		Message string `json:"message"`
	}
	if json.Unmarshal([]byte(e.Body), &parsed) == nil && parsed.Message != "" {
		msg = parsed.Message
	}
	hint := ""
	switch e.Status {
	case http.StatusUnauthorized:
		hint = " (the bot token is invalid or expired)"
	case http.StatusForbidden, http.StatusNotFound:
		hint = " (is the bot a member of this space?)"
	}
	return fmt.Sprintf("webex API returned HTTP %d: %s%s", e.Status, strings.TrimSpace(msg), hint)
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	u := strings.TrimRight(c.BaseURL, "/") + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("webex API %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return &APIError{Status: resp.StatusCode, Body: string(data)}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

// Me returns the identity behind the token.
func (c *Client) Me(ctx context.Context) (*Person, error) {
	var p Person
	if err := c.do(ctx, http.MethodGet, "/people/me", nil, nil, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// Room returns one room the bot can see.
func (c *Client) Room(ctx context.Context, id string) (*Room, error) {
	var r Room
	if err := c.do(ctx, http.MethodGet, "/rooms/"+url.PathEscape(id), nil, nil, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// Rooms lists the rooms the bot is a member of.
func (c *Client) Rooms(ctx context.Context, maxRooms int) ([]Room, error) {
	var out struct {
		Items []Room `json:"items"`
	}
	q := url.Values{"max": {strconv.Itoa(maxRooms)}, "sortBy": {"lastactivity"}}
	if err := c.do(ctx, http.MethodGet, "/rooms", q, nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// Messages lists the newest messages of a room, newest first. Bots can only list group
// space messages that mention them, so mentionedOnly must be true for group rooms.
func (c *Client) Messages(ctx context.Context, roomID string, mentionedOnly bool, maxMessages int) ([]Message, error) {
	q := url.Values{"roomId": {roomID}, "max": {strconv.Itoa(maxMessages)}}
	if mentionedOnly {
		q.Set("mentionedPeople", "me")
	}
	var out struct {
		Items []Message `json:"items"`
	}
	if err := c.do(ctx, http.MethodGet, "/messages", q, nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// Message fetches one message by ID (webhooks only carry the ID).
func (c *Client) Message(ctx context.Context, id string) (*Message, error) {
	var m Message
	if err := c.do(ctx, http.MethodGet, "/messages/"+url.PathEscape(id), nil, nil, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// Send posts a markdown message, threaded under parentID when it is set.
func (c *Client) Send(ctx context.Context, roomID, parentID, markdown string) error {
	payload := map[string]string{"roomId": roomID, "markdown": markdown}
	if parentID != "" {
		payload["parentId"] = parentID
	}
	return c.do(ctx, http.MethodPost, "/messages", nil, payload, nil)
}
