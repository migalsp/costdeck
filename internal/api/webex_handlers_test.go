package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // Mirrors the Webex signature scheme under test.
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/webex"
)

func sign(body []byte, secret string) string {
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestWebexWebhookRequiresValidSignature(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServer()
	ctx := context.Background()
	if err := server.Client.Create(ctx, &corev1.Secret{
		Name: "wx", Namespace: "costdeck",
		Data: map[string][]byte{"BOT_TOKEN": []byte("t"), "WEBHOOK_SECRET": []byte("s3cr3t")},
	}); err != nil {
		t.Fatal(err)
	}
	if err := server.Client.Create(ctx, &finopsv1.CostDeckConfig{
		Name: "default", Namespace: "costdeck",
		Spec: finopsv1.CostDeckConfigSpec{Integrations: finopsv1.IntegrationsConfig{
			Messenger: &finopsv1.MessengerIntegrationConfig{Webex: &finopsv1.WebexConfig{Enabled: true, SecretRef: "wx"}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	handler, err := server.Handler()
	if err != nil {
		t.Fatal(err)
	}

	// An attachment action is acknowledged without fetching anything from Webex.
	body := []byte(`{"resource":"attachmentActions","event":"created","data":{"id":"x"}}`)
	post := func(signature string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/webex/webhook", bytes.NewReader(body))
		if signature != "" {
			req.Header.Set("X-Spark-Signature", signature)
		}
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		return rr.Code
	}

	if code := post(""); code != http.StatusUnauthorized {
		t.Errorf("unsigned webhook returned %d, want 401", code)
	}
	if code := post(sign(body, "wrong")); code != http.StatusUnauthorized {
		t.Errorf("badly signed webhook returned %d, want 401", code)
	}
	if code := post(sign(body, "s3cr3t")); code != http.StatusNoContent {
		t.Errorf("correctly signed webhook returned %d, want 204", code)
	}
}

type webexReply struct {
	RoomID   string `json:"roomId"`
	ParentID string `json:"parentId"`
	Markdown string `json:"markdown"`
}

func webexBot(t *testing.T, message map[string]any) (*requestLog, chan webexReply, string) {
	t.Helper()
	log := &requestLog{}
	replies := make(chan webexReply, 4)
	url := fakeWebexAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		log.add(r)
		if r.Header.Get("Authorization") != "Bearer bot-token" {
			http.Error(w, `{"message":"invalid token"}`, http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == webexMePath:
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "bot-id", "displayName": "CostDeck Bot", "emails": []string{"bot@webex.bot"}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/messages/msg-1":
			_ = json.NewEncoder(w).Encode(message)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/messages":
			var reply webexReply
			_ = json.Unmarshal(body, &reply)
			replies <- reply
			_, _ = w.Write([]byte(`{"id":"reply-1"}`))
		default:
			http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
		}
	}))
	return log, replies, url
}

func webexServer(t *testing.T, token string) *Server {
	t.Helper()
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServer()
	createAll(t, server.Client,
		&corev1.Secret{Name: "wx", Namespace: "costdeck", Data: map[string][]byte{"BOT_TOKEN": []byte(token), "WEBHOOK_SECRET": []byte("s3cr3t")}},
		&finopsv1.CostDeckConfig{Name: "default", Namespace: "costdeck", Spec: finopsv1.CostDeckConfigSpec{Integrations: finopsv1.IntegrationsConfig{
			Messenger: &finopsv1.MessengerIntegrationConfig{Webex: &finopsv1.WebexConfig{Enabled: true, SecretRef: "wx", RoomID: "room-ops"}},
		}}},
		&finopsv1.ScalingGroup{Name: "nightly", Namespace: "costdeck", Spec: finopsv1.ScalingGroupSpec{Namespaces: []string{"shop-dev"}}},
	)
	return server
}

func postWebhook(t *testing.T, server *Server, body string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/webex/webhook", bytes.NewBufferString(body))
	req.Header.Set("X-Spark-Signature", sign([]byte(body), "s3cr3t"))
	rr := httptest.NewRecorder()
	server.routes().ServeHTTP(rr, req)
	return rr.Code
}

func TestWebexWebhookMessageIsAnsweredInItsThread(t *testing.T) {
	calls, replies, url := webexBot(t, map[string]any{"id": "msg-1", "roomId": "room-ops", "roomType": "group", "text": "CostDeck Bot status group nightly", "personEmail": "anna@example.com"})
	server := webexServer(t, "bot-token")
	server.WebexAPI = url

	if code := postWebhook(t, server, `{"resource":"messages","event":"created","data":{"id":"msg-1","roomId":"room-elsewhere"}}`); code != http.StatusNoContent {
		t.Errorf("a message from another space = %d, want 204", code)
	}
	if c := calls.all(); len(c) != 0 {
		t.Fatalf("a message from another space was fetched: %+v", c)
	}

	if code := postWebhook(t, server, `{"resource":"messages","event":"created","data":{"id":"msg-1","roomId":"room-ops"}}`); code != http.StatusAccepted {
		t.Fatalf("a message from the configured space = %d, want 202", code)
	}
	select {
	case reply := <-replies:
		if reply.RoomID != "room-ops" || reply.ParentID != "msg-1" || !strings.Contains(reply.Markdown, "**Group `nightly`**") || !strings.Contains(reply.Markdown, "`shop-dev`") {
			t.Errorf("reply = %+v, want the group's status threaded under the message", reply)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the bot never answered; Webex calls: %+v", calls.all())
	}
}

func TestWebexMessageIsDroppedWhenTheBotCannotSignIn(t *testing.T) {
	calls, replies, url := webexBot(t, map[string]any{"id": "msg-1", "roomId": "room-ops", "text": "help"})
	server := webexServer(t, "revoked-token")
	server.WebexAPI = url

	server.processWebexMessage(&webex.Settings{Token: "revoked-token", RoomID: "room-ops"}, "msg-1")
	c := calls.all()
	if len(c) != 1 || c[0].Path != webexMePath {
		t.Errorf("Webex calls = %+v, want only the failed identity check", c)
	}
	if len(replies) != 0 {
		t.Error("a bot that cannot sign in must not answer")
	}
}

func TestWebexWebhookRunsAReplayedDeliveryOnce(t *testing.T) {
	_, replies, url := webexBot(t, map[string]any{"id": "msg-1", "roomId": "room-ops", "roomType": "group", "text": "CostDeck Bot status group nightly"})
	server := webexServer(t, "bot-token")
	server.WebexAPI = url
	delivery := `{"resource":"messages","event":"created","data":{"id":"msg-1","roomId":"room-ops"}}`

	if code := postWebhook(t, server, delivery); code != http.StatusAccepted {
		t.Fatalf("first delivery = %d, want 202", code)
	}
	select {
	case <-replies:
	case <-time.After(10 * time.Second):
		t.Fatal("the bot never answered the first delivery")
	}
	if code := postWebhook(t, server, delivery); code != http.StatusNoContent {
		t.Errorf("the same signed delivery again = %d, want 204 without running the command again", code)
	}
	select {
	case reply := <-replies:
		t.Errorf("a replayed delivery ran the command again: %+v", reply)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestRecentDeliveriesStayBounded(t *testing.T) {
	var d recentDeliveries
	now := time.Now()
	for i := range deliveriesKept + 50 {
		if !d.first(fmt.Sprintf("msg-%d", i), now) {
			t.Fatalf("msg-%d counted as seen", i)
		}
	}
	if len(d.seen) > deliveriesKept {
		t.Errorf("%d message ids kept, want at most %d", len(d.seen), deliveriesKept)
	}
	if !d.first("msg-0", now.Add(deliveryWindow)) {
		t.Error("a message id is remembered past the replay window")
	}
}
