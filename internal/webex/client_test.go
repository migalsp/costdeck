package webex

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAPIErrorMessages(t *testing.T) {
	tests := []struct {
		err  APIError
		want string
	}{
		{APIError{Status: http.StatusUnauthorized, Body: `{"message":"The request requires a valid access token set in the Authorization request header."}`},
			"webex API returned HTTP 401: The request requires a valid access token set in the Authorization request header. (the bot token is invalid or expired)"},
		{APIError{Status: http.StatusForbidden, Body: `{"message":"Failed to post message."}`}, "webex API returned HTTP 403: Failed to post message. (is the bot a member of this space?)"},
		{APIError{Status: http.StatusNotFound, Body: `{"message":"The requested resource could not be found."}`}, "webex API returned HTTP 404: The requested resource could not be found. (is the bot a member of this space?)"},
		{APIError{Status: http.StatusBadGateway, Body: "  <html>bad gateway</html>\n"}, "webex API returned HTTP 502: <html>bad gateway</html>"},
		{APIError{Status: http.StatusTooManyRequests, Body: `{"message":""}`}, `webex API returned HTTP 429: {"message":""}`},
	}
	for _, tt := range tests {
		if got := tt.err.Error(); got != tt.want {
			t.Errorf("APIError{%d}.Error() = %q\nwant %q", tt.err.Status, got, tt.want)
		}
	}
}

type webexCall struct {
	method, path, query, auth, contentType string
	body                                   map[string]string
}

type recordingWebex struct {
	mu    sync.Mutex
	calls []webexCall
	reply func(r *http.Request) (int, string)
}

func (f *recordingWebex) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]string
	_ = json.Unmarshal(raw, &body)
	f.mu.Lock()
	f.calls = append(f.calls, webexCall{r.Method, r.URL.EscapedPath(), r.URL.RawQuery, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), body})
	f.mu.Unlock()
	status, out := http.StatusOK, `{}`
	if f.reply != nil {
		status, out = f.reply(r)
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, out)
}

const (
	pathMe       = "/people/me"
	pathRooms    = "/rooms"
	pathMessages = "/messages"
)

func clientTestReplies(r *http.Request) (int, string) {
	switch {
	case r.URL.Path == pathMe:
		return http.StatusOK, `{"id":"bot","displayName":"CostDeck Bot","emails":["costdeck@webex.bot"]}`
	case r.URL.Path == pathRooms:
		return http.StatusOK, `{"items":[{"id":"r1","title":"Ops","type":"group"},{"id":"r2","title":"Alice","type":"direct"}]}`
	case strings.HasPrefix(r.URL.Path, pathRooms+"/"):
		return http.StatusOK, `{"id":"Y2lz/room==","title":"Ops","type":"group"}`
	case r.URL.Path == pathMessages && r.Method == http.MethodGet:
		return http.StatusOK, `{"items":[{"id":"m1","roomId":"r1","text":"hi","personEmail":"a@example.com","created":"2026-10-31T09:00:00.000Z"}]}`
	case strings.HasPrefix(r.URL.Path, pathMessages+"/"):
		return http.StatusOK, `{"id":"Y2lz/msg","parentId":"root","roomId":"r1","roomType":"group","text":"scale group a up"}`
	}
	return http.StatusOK, `{"id":"sent"}`
}

func TestClientRequests(t *testing.T) {
	api := &recordingWebex{reply: clientTestReplies}
	srv := httptest.NewServer(api)
	defer srv.Close()
	c := &Client{Token: "bot-token", BaseURL: srv.URL + "/", HTTP: srv.Client()}
	ctx := context.Background()

	me, err := c.Me(ctx)
	if err != nil || me.DisplayName != "CostDeck Bot" || me.Emails[0] != "costdeck@webex.bot" {
		t.Errorf("Me() = %+v, %v", me, err)
	}
	room, err := c.Room(ctx, "Y2lz/room==")
	if err != nil || room.Title != "Ops" || room.Type != "group" {
		t.Errorf("Room() = %+v, %v", room, err)
	}
	rooms, err := c.Rooms(ctx, 50)
	if err != nil || len(rooms) != 2 || rooms[1].Type != "direct" {
		t.Errorf("Rooms() = %+v, %v", rooms, err)
	}
	msgs, err := c.Messages(ctx, "r1", true, 20)
	if err != nil || len(msgs) != 1 || msgs[0].PersonEmail != "a@example.com" || !msgs[0].Created.Equal(time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("Messages() = %+v, %v", msgs, err)
	}
	if _, err := c.Messages(ctx, "r2", false, 5); err != nil {
		t.Fatal(err)
	}
	msg, err := c.Message(ctx, "Y2lz/msg")
	if err != nil || msg.ParentID != "root" || msg.Text != "scale group a up" {
		t.Errorf("Message() = %+v, %v", msg, err)
	}
	if err := c.Send(ctx, "r1", "", "**hello**"); err != nil {
		t.Fatal(err)
	}
	if err := c.Send(ctx, "r1", "root", "threaded"); err != nil {
		t.Fatal(err)
	}

	assertWebexCalls(t, api.calls, []webexCall{
		{method: "GET", path: pathMe},
		{method: "GET", path: "/rooms/Y2lz%2Froom=="},
		{method: "GET", path: pathRooms, query: "max=50&sortBy=lastactivity"},
		{method: "GET", path: pathMessages, query: "max=20&mentionedPeople=me&roomId=r1"},
		{method: "GET", path: pathMessages, query: "max=5&roomId=r2"},
		{method: "GET", path: "/messages/Y2lz%2Fmsg"},
		{method: "POST", path: pathMessages, contentType: "application/json", body: map[string]string{"roomId": "r1", "markdown": "**hello**"}},
		{method: "POST", path: pathMessages, contentType: "application/json", body: map[string]string{"roomId": "r1", "markdown": "threaded", "parentId": "root"}},
	})
}

func assertWebexCalls(t *testing.T, got, want []webexCall) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("calls = %+v", got)
	}
	for i, w := range want {
		g := got[i]
		if g.method != w.method || g.path != w.path || g.query != w.query || g.contentType != w.contentType {
			t.Errorf("call %d = %s %s?%s (%s), want %s %s?%s (%s)", i, g.method, g.path, g.query, g.contentType, w.method, w.path, w.query, w.contentType)
		}
		if g.auth != "Bearer bot-token" {
			t.Errorf("call %d sent Authorization %q", i, g.auth)
		}
		if !maps.Equal(g.body, w.body) {
			t.Errorf("call %d body = %v, want %v", i, g.body, w.body)
		}
	}
}

func TestClientErrors(t *testing.T) {
	closed := httptest.NewServer(http.NotFoundHandler())
	url := closed.URL
	closed.Close()
	c := &Client{Token: "t", BaseURL: url, HTTP: http.DefaultClient}
	if _, err := c.Me(context.Background()); err == nil || !strings.HasPrefix(err.Error(), "webex API GET /people/me: ") {
		t.Errorf("Me() on an unreachable API = %v", err)
	}

	api := &recordingWebex{reply: func(r *http.Request) (int, string) {
		if r.URL.Path == pathRooms {
			return http.StatusOK, "<html>captive portal</html>"
		}
		return http.StatusNotFound, `{"message":"The requested resource could not be found."}`
	}}
	srv := httptest.NewServer(api)
	defer srv.Close()
	c = &Client{Token: "t", BaseURL: srv.URL, HTTP: srv.Client()}
	if rooms, err := c.Rooms(context.Background(), 10); rooms != nil || err == nil {
		t.Errorf("Rooms() on a non-JSON answer = %v, %v", rooms, err)
	}
	room, err := c.Room(context.Background(), "gone")
	var apiErr *APIError
	if room != nil || !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Errorf("Room() = %+v, %v; want the APIError", room, err)
	}
	if msg, err := c.Message(context.Background(), "gone"); msg != nil || err == nil {
		t.Errorf("Message() = %+v, %v", msg, err)
	}
}

func TestNewClient(t *testing.T) {
	c := NewClient("tok")
	if c.Token != "tok" || c.BaseURL != DefaultBaseURL || c.HTTP == nil || c.HTTP.Timeout != 15*time.Second {
		t.Errorf("NewClient() = %+v", c)
	}
}
