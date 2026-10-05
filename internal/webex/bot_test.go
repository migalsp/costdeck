package webex

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/scaling"
)

// fakeWebex is an in-memory Webex API.
type fakeWebex struct {
	mu       sync.Mutex
	me       Person
	rooms    []Room
	messages map[string][]Message // by room
	sent     []map[string]string
	sendCode int
}

func (f *fakeWebex) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.URL.Path == "/people/me":
		_ = json.NewEncoder(w).Encode(f.me)
	case r.URL.Path == "/rooms":
		_ = json.NewEncoder(w).Encode(map[string]any{"items": f.rooms})
	case strings.HasPrefix(r.URL.Path, "/rooms/"):
		id := strings.TrimPrefix(r.URL.Path, "/rooms/")
		for _, room := range f.rooms {
			if room.ID == id {
				_ = json.NewEncoder(w).Encode(room)
				return
			}
		}
		http.Error(w, `{"message":"The requested resource could not be found."}`, http.StatusNotFound)
	case r.URL.Path == "/messages" && r.Method == http.MethodGet:
		room := r.URL.Query().Get("roomId")
		msgs := f.messages[room]
		// Newest first, like Webex.
		out := make([]Message, len(msgs))
		for i := range msgs {
			out[i] = msgs[len(msgs)-1-i]
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": out})
	case r.URL.Path == "/messages" && r.Method == http.MethodPost:
		if f.sendCode != 0 {
			http.Error(w, `{"message":"Failed to post message."}`, f.sendCode)
			return
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.sent = append(f.sent, body)
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "sent"})
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeWebex) replies() []map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]string(nil), f.sent...)
}

func newTestBot(t *testing.T, clusterName string, objs ...client.Object) (*Bot, *fakeWebex, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(finopsv1.AddToScheme(scheme))
	k8s := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()

	api := &fakeWebex{me: Person{ID: "bot-id", DisplayName: "FinOps Bot", Emails: []string{"finops@webex.bot"}}, messages: map[string][]Message{}}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)

	return &Bot{
		API:         &Client{Token: "t", BaseURL: srv.URL, HTTP: srv.Client()},
		K8s:         k8s,
		Namespace:   "costdeck",
		ClusterName: clusterName,
		Me:          &api.me,
	}, api, k8s
}

func scheduledGroup(name string) *finopsv1.ScalingGroup {
	return &finopsv1.ScalingGroup{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "costdeck"},
		Spec: finopsv1.ScalingGroupSpec{
			Namespaces: []string{name + "-ns"},
			Schedules:  []finopsv1.ScalingSchedule{{Days: []int{1, 2, 3, 4, 5}, StartTime: "09:00", EndTime: "18:00", Timezone: "UTC"}},
		},
	}
}

func groupMsg(text string) *Message {
	return &Message{ID: "m1", RoomID: "room", RoomType: "group", Text: text, PersonID: "u1", PersonEmail: "dev@example.com"}
}

func TestBotStripsAnyMentionAndScales(t *testing.T) {
	bot, api, k8s := newTestBot(t, "", scheduledGroup("pps1"))

	for _, text := range []string{"FinOps Bot scale group pps1 up", "FinOps scale group pps1 up", "@FinOps Bot, scale group pps1 up"} {
		if err := bot.ProcessMessage(context.Background(), groupMsg(text)); err != nil {
			t.Fatalf("%q: %v", text, err)
		}
	}

	g := &finopsv1.ScalingGroup{}
	if err := k8s.Get(context.Background(), client.ObjectKey{Name: "pps1", Namespace: "costdeck"}, g); err != nil {
		t.Fatal(err)
	}
	if g.Spec.Active == nil || !*g.Spec.Active {
		t.Fatal("scale up must set spec.active=true")
	}
	want := (&scaling.Engine{}).NextScheduleChange(time.Now(), g.Spec.Schedules)
	if g.Spec.ActiveUntil == nil || want == nil || !g.Spec.ActiveUntil.Time.Equal(*want) {
		t.Errorf("without a duration the override must end at the next schedule change, got %v want %v", g.Spec.ActiveUntil, want)
	}
	replies := api.replies()
	if len(replies) != 3 || !strings.Contains(replies[0]["markdown"], "until the next scheduled change") {
		t.Fatalf("unexpected replies: %v", replies)
	}
	if replies[0]["parentId"] != "m1" {
		t.Errorf("replies must be threaded under the command, got parentId %q", replies[0]["parentId"])
	}
}

func TestBotDurationsAndResume(t *testing.T) {
	bot, _, k8s := newTestBot(t, "", scheduledGroup("stag1"))
	ctx := context.Background()
	get := func() *finopsv1.ScalingGroup {
		g := &finopsv1.ScalingGroup{}
		if err := k8s.Get(ctx, client.ObjectKey{Name: "stag1", Namespace: "costdeck"}, g); err != nil {
			t.Fatal(err)
		}
		return g
	}

	if err := bot.ProcessMessage(ctx, groupMsg("FinOps Bot scale group stag1 down for 2h")); err != nil {
		t.Fatal(err)
	}
	if g := get(); g.Spec.ActiveUntil == nil || time.Until(g.Spec.ActiveUntil.Time) < 119*time.Minute {
		t.Errorf("for 2h must hold about two hours, got %v", g.Spec.ActiveUntil)
	}

	if err := bot.ProcessMessage(ctx, groupMsg("FinOps Bot scale group stag1 up forever")); err != nil {
		t.Fatal(err)
	}
	if g := get(); g.Spec.ActiveUntil != nil || g.Spec.Active == nil || !*g.Spec.Active {
		t.Errorf("forever must leave activeUntil unset, got active=%v until=%v", g.Spec.Active, g.Spec.ActiveUntil)
	}

	if err := bot.ProcessMessage(ctx, groupMsg("FinOps Bot resume group stag1")); err != nil {
		t.Fatal(err)
	}
	if g := get(); g.Spec.Active != nil {
		t.Errorf("resume must clear spec.active, got %v", *g.Spec.Active)
	}
}

func TestBotAnswersUnknownCommands(t *testing.T) {
	bot, api, _ := newTestBot(t, "")
	if err := bot.ProcessMessage(context.Background(), groupMsg("FinOps Bot what's up")); err != nil {
		t.Fatal(err)
	}
	replies := api.replies()
	if len(replies) != 1 || !strings.Contains(replies[0]["markdown"], "did not understand") {
		t.Fatalf("an unknown command must get the help text, got %v", replies)
	}
}

func TestBotMultiClusterRouting(t *testing.T) {
	bot, api, _ := newTestBot(t, "prod", scheduledGroup("pps1"))
	ctx := context.Background()

	// No cluster prefix, but the group exists here: act on it.
	if err := bot.ProcessMessage(ctx, groupMsg("FinOps Bot status group pps1")); err != nil {
		t.Fatal(err)
	}
	// No prefix and unknown here: another cluster in the space may own it.
	if err := bot.ProcessMessage(ctx, groupMsg("FinOps Bot status group stag1")); err != nil {
		t.Fatal(err)
	}
	// Explicitly addressed to another cluster.
	if err := bot.ProcessMessage(ctx, groupMsg("FinOps Bot dev scale group pps1 up")); err != nil {
		t.Fatal(err)
	}
	// Explicitly addressed here: even a missing group gets an answer.
	if err := bot.ProcessMessage(ctx, groupMsg("FinOps Bot prod status group stag1")); err != nil {
		t.Fatal(err)
	}

	replies := api.replies()
	if len(replies) != 2 {
		t.Fatalf("got %d replies, want 2: %v", len(replies), replies)
	}
	if !strings.HasPrefix(replies[0]["markdown"], "**[prod]**") || !strings.Contains(replies[0]["markdown"], "pps1") {
		t.Errorf("first reply = %q", replies[0]["markdown"])
	}
	if !strings.Contains(replies[1]["markdown"], "not found") {
		t.Errorf("second reply = %q, want a not-found answer", replies[1]["markdown"])
	}
}

func TestBotIgnoresItselfAndReportsSendFailures(t *testing.T) {
	bot, api, _ := newTestBot(t, "")
	own := groupMsg("FinOps Bot help")
	own.PersonID = "bot-id"
	if err := bot.ProcessMessage(context.Background(), own); err != nil || len(api.replies()) != 0 {
		t.Fatalf("the bot must ignore its own messages (err=%v replies=%v)", err, api.replies())
	}

	api.sendCode = http.StatusForbidden
	err := bot.ProcessMessage(context.Background(), groupMsg("FinOps Bot help"))
	if err == nil || !strings.Contains(err.Error(), "member of this space") {
		t.Fatalf("a failed reply must surface with a hint, got %v", err)
	}
}

func TestPollerProcessesOnlyNewMessages(t *testing.T) {
	_, api, k8s := newTestBot(t, "")
	srv := httptest.NewServer(api)
	defer srv.Close()
	t.Setenv("POD_NAMESPACE", "costdeck")
	ctx := context.Background()

	api.rooms = []Room{{ID: "room", Title: "Ops", Type: "group"}}
	api.messages["room"] = []Message{{ID: "old", RoomID: "room", Text: "FinOps Bot help", PersonID: "u1", Created: time.Now().Add(-time.Hour)}}
	for _, obj := range []client.Object{
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "wx", Namespace: "costdeck"}, Data: map[string][]byte{SecretKeyBotToken: []byte("t")}},
		&finopsv1.CostDeckConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "costdeck"},
			Spec: finopsv1.CostDeckConfigSpec{Integrations: finopsv1.IntegrationsConfig{
				Messenger: &finopsv1.MessengerIntegrationConfig{Webex: &finopsv1.WebexConfig{Enabled: true, SecretRef: "wx"}},
			}},
		},
	} {
		if err := k8s.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}

	p := &Poller{Client: k8s, NewClient: func(token string) *Client { return &Client{Token: token, BaseURL: srv.URL, HTTP: srv.Client()} }}
	p.Poll(ctx)
	if n := len(api.replies()); n != 0 {
		t.Fatalf("the first round must not replay history, got %d replies", n)
	}

	api.mu.Lock()
	api.messages["room"] = append(api.messages["room"],
		Message{ID: "new", RoomID: "room", Text: "FinOps Bot help", PersonID: "u1", Created: time.Now().Add(time.Second)},
		Message{ID: "mine", RoomID: "room", Text: "**CostDeck commands**", PersonID: "bot-id", Created: time.Now().Add(2 * time.Second)},
	)
	api.mu.Unlock()
	p.Poll(ctx)
	p.Poll(ctx)
	if n := len(api.replies()); n != 1 {
		t.Fatalf("got %d replies, want exactly one for the new message", n)
	}
}

func TestParseUntil(t *testing.T) {
	tests := map[string]string{
		"":              "",
		"for 4h":        "4h0m0s",
		"30m":           "30m0s",
		"until next":    "nextTransition",
		"forever":       "forever",
		"for a while":   "invalid:a while",
		"for 1h30m":     "1h30m0s",
		"until resumed": "forever",
	}
	for in, want := range tests {
		if got := parseUntil(strings.Fields(in)); got != want {
			t.Errorf("parseUntil(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNotifierPostsOnlyWhenEnabled(t *testing.T) {
	_, api, k8s := newTestBot(t, "prod")
	srv := httptest.NewServer(api)
	defer srv.Close()
	t.Setenv("POD_NAMESPACE", "costdeck")
	ctx := context.Background()
	cfg := &finopsv1.CostDeckConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "costdeck"},
		Spec: finopsv1.CostDeckConfigSpec{ClusterName: "prod", Integrations: finopsv1.IntegrationsConfig{
			Messenger: &finopsv1.MessengerIntegrationConfig{Webex: &finopsv1.WebexConfig{Enabled: true, SecretRef: "wx", RoomID: "room"}},
		}},
	}
	for _, obj := range []client.Object{
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "wx", Namespace: "costdeck"}, Data: map[string][]byte{SecretKeyBotToken: []byte("t")}},
		cfg,
	} {
		if err := k8s.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	n := &Notifier{Client: k8s, NewClient: func(token string) *Client { return &Client{Token: token, BaseURL: srv.URL, HTTP: srv.Client()} }}

	n.Notify(ctx, "⏸️ Group `pps1` is scaled down")
	if len(api.replies()) != 0 {
		t.Fatal("notifications are opt-in")
	}

	cfg.Spec.Integrations.Messenger.Webex.NotifyTransitions = true
	if err := k8s.Update(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	n.Notify(ctx, "⏸️ Group `pps1` is scaled down")
	replies := api.replies()
	if len(replies) != 1 || replies[0]["roomId"] != "room" || !strings.HasPrefix(replies[0]["markdown"], "**[prod]**") {
		t.Errorf("notification = %v", replies)
	}
}
