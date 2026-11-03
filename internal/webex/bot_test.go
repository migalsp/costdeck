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
		SpaceID:     "room",
		Me:          &api.me,
	}, api, k8s
}

func scheduledGroup(name string) *finopsv1.ScalingGroup {
	return &finopsv1.ScalingGroup{
		Name: name, Namespace: "costdeck",
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
		&corev1.Secret{Name: "wx", Namespace: "costdeck", Data: map[string][]byte{SecretKeyBotToken: []byte("t")}},
		&finopsv1.CostDeckConfig{
			Name: "default", Namespace: "costdeck",
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
		Name: "default", Namespace: "costdeck",
		Spec: finopsv1.CostDeckConfigSpec{ClusterName: "prod", Integrations: finopsv1.IntegrationsConfig{
			Messenger: &finopsv1.MessengerIntegrationConfig{Webex: &finopsv1.WebexConfig{Enabled: true, SecretRef: "wx", RoomID: "room"}},
		}},
	}
	for _, obj := range []client.Object{
		&corev1.Secret{Name: "wx", Namespace: "costdeck", Data: map[string][]byte{SecretKeyBotToken: []byte("t")}},
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

func TestBotScalesOnlyInTheConfiguredSpace(t *testing.T) {
	for _, tc := range []struct {
		name    string
		spaceID string
		msg     *Message
		refusal string
	}{
		{"no space configured", "", groupMsg("FinOps Bot scale group pps1 down"), "turned off until a CostDeck space is configured"},
		{"direct message", "room", &Message{ID: "d1", RoomID: "dm", RoomType: "direct", Text: "scale group pps1 down", PersonID: "stranger"}, "only accepted in the CostDeck space"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bot, api, k8s := newTestBot(t, "", scheduledGroup("pps1"))
			bot.SpaceID = tc.spaceID
			if err := bot.ProcessMessage(context.Background(), tc.msg); err != nil {
				t.Fatal(err)
			}
			g := &finopsv1.ScalingGroup{}
			if err := k8s.Get(context.Background(), client.ObjectKey{Name: "pps1", Namespace: "costdeck"}, g); err != nil {
				t.Fatal(err)
			}
			if g.Spec.Active != nil {
				t.Errorf("the group was overridden from %s", tc.name)
			}
			replies := api.replies()
			if len(replies) == 0 || !strings.Contains(replies[len(replies)-1]["markdown"], tc.refusal) {
				t.Errorf("replies = %v, want a refusal containing %q", replies, tc.refusal)
			}
		})
	}
}

func lastReply(t *testing.T, api *fakeWebex) map[string]string {
	t.Helper()
	replies := api.replies()
	if len(replies) == 0 {
		t.Fatal("no reply")
	}
	return replies[len(replies)-1]
}

func TestBotListsGroupsAndConfigs(t *testing.T) {
	down := &finopsv1.ScalingGroup{Name: "b-group", Namespace: "costdeck"}
	down.Status.Phase = scaling.PhaseScaledUp
	down.Status.Mode = scaling.ModeManualUp
	down.Status.NamespacesReady, down.Status.NamespacesTotal = 1, 2
	down.Status.NextTransition = &finopsv1.ScheduledTransition{DesiredState: "Down", Time: metav1.NewTime(time.Date(2026, 11, 2, 18, 0, 0, 0, time.UTC))}
	fresh := &finopsv1.ScalingGroup{Name: "a-group", Namespace: "costdeck"}
	cfg := &finopsv1.ScalingConfig{Name: "shop-config", Namespace: "costdeck", Spec: finopsv1.ScalingConfigSpec{TargetNamespace: "shop"}}
	cfg.Status.Phase = scaling.PhaseScaledDown
	cfg.Status.Mode = scaling.ModeSchedule
	elsewhere := &finopsv1.ScalingGroup{Name: "other-ns", Namespace: "default"}
	bot, api, _ := newTestBot(t, "", down, fresh, cfg, elsewhere)

	for _, text := range []string{"FinOps Bot list", "FinOps Bot groups", "FinOps Bot overview", "FinOps Bot status", "FinOps Bot status namespaces"} {
		if err := bot.ProcessMessage(context.Background(), groupMsg(text)); err != nil {
			t.Fatal(err)
		}
		got := lastReply(t, api)["markdown"]
		want := "**Groups**\n" +
			"- `a-group` **Pending** (unknown) · 0/0 ready\n" +
			"- `b-group` **ScaledUp** (manual) · 1/2 ready · down at Mon 18:00 UTC\n" +
			"\n**Namespace configs**\n" +
			"- `shop-config` (shop) **ScaledDown** (schedule)\n"
		if got != want {
			t.Errorf("%q answered\n%s\nwant\n%s", text, got, want)
		}
	}

	empty, emptyAPI, _ := newTestBot(t, "")
	if err := empty.ProcessMessage(context.Background(), groupMsg("FinOps Bot list")); err != nil {
		t.Fatal(err)
	}
	if got := lastReply(t, emptyAPI)["markdown"]; got != "No ScalingGroups or ScalingConfigs are defined yet." {
		t.Errorf("empty list = %q", got)
	}
}

func TestBotStatus(t *testing.T) {
	g := scheduledGroup("pps1")
	g.Spec.DependsOn = []string{"db"}
	g.Spec.NamespaceSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"team": "pps"}}
	g.Status.RequiredBy = []string{"frontend"}
	g.Status.SelectedNamespaces = []string{"pps1-extra"}
	g.Status.Phase = scaling.PhaseScaledUp
	cfg := &finopsv1.ScalingConfig{Name: "shop-config", Namespace: "costdeck", Spec: finopsv1.ScalingConfigSpec{TargetNamespace: "shop"}}
	cfg.Status.Phase = scaling.PhaseScaledDown
	tests := []struct {
		cluster string
		text    string
		want    []string
		silent  bool
	}{
		{text: "FinOps Bot status group pps1", want: []string{"**Group `pps1`**", "**ScaledUp** (unknown)", "Namespaces: 0/0 ready", "Depends on: db", "Kept up for: frontend", "- `pps1-extra`\n", "- `pps1-ns`\n"}},
		{text: "FinOps Bot status config shop-config", want: []string{"**Config `shop-config`** (shop)", "**ScaledDown** (unknown)"}},
		{text: "FinOps Bot status config shop", want: []string{"**Config `shop-config`** (shop)"}},
		{text: "FinOps Bot status group", want: []string{"⚠️ which group? usage: `status group <name>`"}},
		{text: "FinOps Bot status config", want: []string{"⚠️ which namespace? usage: `status config <namespace>`"}},
		{text: "FinOps Bot status cluster prod", want: []string{"⚠️ usage: `status group <name>` or `status config <name>`"}},
		{text: "FinOps Bot status config payments", want: []string{"⚠️ no namespace config `payments`"}},
		{cluster: "prod", text: "FinOps Bot status config payments", silent: true},
		{cluster: "prod", text: "FinOps Bot prod status config payments", want: []string{"**[prod]** ⚠️ no namespace config `payments`"}},
	}
	for _, tt := range tests {
		t.Run(tt.cluster+" "+tt.text, func(t *testing.T) {
			bot, api, _ := newTestBot(t, tt.cluster, g.DeepCopy(), cfg.DeepCopy())
			if err := bot.ProcessMessage(context.Background(), groupMsg(tt.text)); err != nil {
				t.Fatal(err)
			}
			replies := api.replies()
			if tt.silent {
				if len(replies) != 0 {
					t.Errorf("replies = %v; another cluster in the space may own it", replies)
				}
				return
			}
			if len(replies) != 1 {
				t.Fatalf("replies = %v", replies)
			}
			for _, want := range tt.want {
				if !strings.Contains(replies[0]["markdown"], want) {
					t.Errorf("reply %q lacks %q", replies[0]["markdown"], want)
				}
			}
		})
	}
}

func TestBotScalesAConfigFoundByItsNamespace(t *testing.T) {
	cfg := &finopsv1.ScalingConfig{Name: "shop-config", Namespace: "costdeck", Spec: finopsv1.ScalingConfigSpec{TargetNamespace: "shop"}}
	bot, api, k8s := newTestBot(t, "", cfg)
	before := time.Now()
	if err := bot.ProcessMessage(context.Background(), groupMsg("FinOps Bot scale config shop down for 90m")); err != nil {
		t.Fatal(err)
	}
	got := &finopsv1.ScalingConfig{}
	if err := k8s.Get(context.Background(), client.ObjectKey{Name: "shop-config", Namespace: "costdeck"}, got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.Active == nil || *got.Spec.Active {
		t.Fatalf("spec.active = %v, want false", got.Spec.Active)
	}
	if until := got.Spec.ActiveUntil; until == nil || until.Time.Before(before.Add(89*time.Minute)) || until.After(time.Now().Add(90*time.Minute)) {
		t.Errorf("activeUntil = %v, want about 90 minutes from now", until)
	}
	reply := lastReply(t, api)["markdown"]
	if !strings.Contains(reply, "Scaling config `shop-config` down") || !strings.Contains(reply, "holding for 1h30m0s") {
		t.Errorf("reply = %q", reply)
	}
}

func TestBotScaleUsageErrors(t *testing.T) {
	tests := []struct{ text, want string }{
		{"FinOps Bot scale group pps1 sideways", "⚠️ usage: `scale group <name> up|down [for 4h | until next | forever]`"},
		{"FinOps Bot scale", "⚠️ usage: `scale group <name> up|down"},
		{"FinOps Bot scale config shop sideways", "⚠️ usage: `scale config <name> up|down"},
		{"FinOps Bot scale cluster pps1 up", "⚠️ usage: `scale group <name> up|down` or `scale config <namespace> up|down`"},
		{"FinOps Bot scale group pps1 up for a while", "⚠️ could not read \"a while\" as a duration; use e.g. `for 4h`, `until next` or `forever`"},
		{"FinOps Bot scale group pps1 up for -2h", "⚠️ could not read \"-2h\" as a duration"},
		{"FinOps Bot scale group missing up", "⚠️ group `missing` not found"},
		{"FinOps Bot scale config missing up", "⚠️ no namespace config `missing`"},
		{"FinOps Bot resume cluster pps1", "⚠️ usage: `resume group <name>` or `resume config <namespace>`"},
		{"FinOps Bot resume group", "⚠️ which group? usage: `resume group <name>`"},
	}
	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			bot, api, k8s := newTestBot(t, "", scheduledGroup("pps1"))
			if err := bot.ProcessMessage(context.Background(), groupMsg(tt.text)); err != nil {
				t.Fatal(err)
			}
			if got := lastReply(t, api)["markdown"]; !strings.HasPrefix(got, tt.want) {
				t.Errorf("reply = %q, want it to start with %q", got, tt.want)
			}
			g := &finopsv1.ScalingGroup{}
			if err := k8s.Get(context.Background(), client.ObjectKey{Name: "pps1", Namespace: "costdeck"}, g); err != nil {
				t.Fatal(err)
			}
			if g.Spec.Active != nil || g.Spec.ActiveUntil != nil {
				t.Errorf("a refused command changed the group: active=%v until=%v", g.Spec.Active, g.Spec.ActiveUntil)
			}
		})
	}
}

func TestBotResumeClearsEveryOverride(t *testing.T) {
	yes := true
	until := metav1.NewTime(time.Now().Add(time.Hour))
	cfg := &finopsv1.ScalingConfig{
		Name: "shop-config", Namespace: "costdeck",
		Annotations: map[string]string{scaling.LegacyOverrideAnnotation: "up", "keep": "me"},
		Spec:        finopsv1.ScalingConfigSpec{TargetNamespace: "shop", Active: &yes, ActiveUntil: &until},
	}
	g := scheduledGroup("pps1")
	g.Spec.Active = &yes
	bot, api, k8s := newTestBot(t, "", cfg, g)
	ctx := context.Background()

	if err := bot.ProcessMessage(ctx, groupMsg("FinOps Bot resume config shop")); err != nil {
		t.Fatal(err)
	}
	got := &finopsv1.ScalingConfig{}
	if err := k8s.Get(ctx, client.ObjectKey{Name: "shop-config", Namespace: "costdeck"}, got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.Active != nil || got.Spec.ActiveUntil != nil || got.Annotations[scaling.LegacyOverrideAnnotation] != "" || got.Annotations["keep"] != "me" {
		t.Errorf("after resume: active=%v until=%v annotations=%v", got.Spec.Active, got.Spec.ActiveUntil, got.Annotations)
	}
	if reply := lastReply(t, api)["markdown"]; reply != "📅 Config `shop-config` follows its schedule again." {
		t.Errorf("reply = %q", reply)
	}

	for _, alias := range []string{"follow", "schedule"} {
		if err := bot.ProcessMessage(ctx, groupMsg("FinOps Bot "+alias+" group pps1")); err != nil {
			t.Fatal(err)
		}
		if reply := lastReply(t, api)["markdown"]; reply != "📅 Group `pps1` follows its schedule again." {
			t.Errorf("%s: reply = %q", alias, reply)
		}
	}
	group := &finopsv1.ScalingGroup{}
	if err := k8s.Get(ctx, client.ObjectKey{Name: "pps1", Namespace: "costdeck"}, group); err != nil || group.Spec.Active != nil {
		t.Errorf("group after resume: active=%v err=%v", group.Spec.Active, err)
	}
}

func TestBotHoldsUntilResumedWithoutASchedule(t *testing.T) {
	for _, tt := range []struct{ text, want string }{
		{"FinOps Bot scale group always-on up", "holding until you `resume` the schedule"},
		{"FinOps Bot scale group always-on up until next", "holding until you `resume` (the schedule never changes)"},
	} {
		t.Run(tt.text, func(t *testing.T) {
			bot, api, k8s := newTestBot(t, "", &finopsv1.ScalingGroup{Name: "always-on", Namespace: "costdeck", Spec: finopsv1.ScalingGroupSpec{Namespaces: []string{"a"}}})
			if err := bot.ProcessMessage(context.Background(), groupMsg(tt.text)); err != nil {
				t.Fatal(err)
			}
			g := &finopsv1.ScalingGroup{}
			if err := k8s.Get(context.Background(), client.ObjectKey{Name: "always-on", Namespace: "costdeck"}, g); err != nil {
				t.Fatal(err)
			}
			if g.Spec.Active == nil || !*g.Spec.Active || g.Spec.ActiveUntil != nil {
				t.Errorf("active=%v until=%v; without a schedule change the override has no end", g.Spec.Active, g.Spec.ActiveUntil)
			}
			if reply := lastReply(t, api)["markdown"]; !strings.Contains(reply, tt.want) {
				t.Errorf("reply = %q, want %q", reply, tt.want)
			}
		})
	}
}

func waitForReplies(t *testing.T, api *fakeWebex, n int, within time.Duration) []map[string]string {
	t.Helper()
	deadline := time.Now().Add(within)
	for len(api.replies()) < n && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	return api.replies()
}

func TestBotMonitorReportsProgressInTheThread(t *testing.T) {
	setPhase := func(t *testing.T, k8s client.Client, phase string, active *bool) {
		t.Helper()
		g := &finopsv1.ScalingGroup{}
		if err := k8s.Get(context.Background(), client.ObjectKey{Name: "pps1", Namespace: "costdeck"}, g); err != nil {
			t.Fatal(err)
		}
		g.Status.Phase = phase
		if active != nil {
			g.Spec.Active = active
		}
		if err := k8s.Update(context.Background(), g); err != nil {
			t.Fatal(err)
		}
	}
	threaded := &Message{ID: "reply-1", ParentID: "root-1", RoomID: "room", RoomType: "group", Text: "FinOps Bot scale group pps1 up for 1h", PersonID: "u1"}

	t.Run("target reached", func(t *testing.T) {
		bot, api, k8s := newTestBot(t, "prod", scheduledGroup("pps1"))
		bot.Background, bot.MonitorInterval, bot.MonitorTimeout = t.Context(), 5*time.Millisecond, 5*time.Second
		if err := bot.ProcessMessage(context.Background(), threaded); err != nil {
			t.Fatal(err)
		}
		setPhase(t, k8s, scaling.PhaseScaledUp, nil)
		replies := waitForReplies(t, api, 2, 5*time.Second)
		if len(replies) != 2 || replies[1]["markdown"] != "**[prod]** ✅ Group `pps1` is ScaledUp." || replies[1]["parentId"] != "root-1" || replies[0]["parentId"] != "root-1" {
			t.Errorf("replies = %v; progress goes to the root of the command's thread", replies)
		}
	})

	t.Run("target not reached in time", func(t *testing.T) {
		bot, api, _ := newTestBot(t, "", scheduledGroup("pps1"))
		bot.Background, bot.MonitorInterval, bot.MonitorTimeout = context.Background(), 5*time.Millisecond, 40*time.Millisecond
		if err := bot.ProcessMessage(context.Background(), groupMsg("FinOps Bot scale group pps1 down for 1h")); err != nil {
			t.Fatal(err)
		}
		replies := waitForReplies(t, api, 2, 5*time.Second)
		if len(replies) != 2 || !strings.HasPrefix(replies[1]["markdown"], "⏳ Group `pps1` has not reached ScaledDown after 40ms") || !strings.Contains(replies[1]["markdown"], "check `status group pps1`") {
			t.Errorf("replies = %v", replies)
		}
	})

	t.Run("overridden by a newer command", func(t *testing.T) {
		bot, api, k8s := newTestBot(t, "", scheduledGroup("pps1"))
		bot.Background, bot.MonitorInterval, bot.MonitorTimeout = context.Background(), 5*time.Millisecond, 100*time.Millisecond
		if err := bot.ProcessMessage(context.Background(), groupMsg("FinOps Bot scale group pps1 up for 1h")); err != nil {
			t.Fatal(err)
		}
		no := false
		setPhase(t, k8s, scaling.PhaseScaledUp, &no)
		time.Sleep(300 * time.Millisecond)
		if replies := api.replies(); len(replies) != 1 {
			t.Errorf("replies = %v; the newer command reports instead", replies)
		}
	})

	t.Run("no background context", func(t *testing.T) {
		bot, api, _ := newTestBot(t, "", scheduledGroup("pps1"))
		bot.MonitorInterval, bot.MonitorTimeout = time.Millisecond, 10*time.Millisecond
		if err := bot.ProcessMessage(context.Background(), groupMsg("FinOps Bot scale group pps1 up")); err != nil {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
		if replies := api.replies(); len(replies) != 1 {
			t.Errorf("replies = %v; without a background context nothing is monitored", replies)
		}
	})
}

func TestBotHelpVariants(t *testing.T) {
	for _, tt := range []struct {
		cluster, text, prefix string
	}{
		{"", "FinOps Bot", ""},
		{"", "FinOps Bot /help", ""},
		{"", "FinOps Bot ?", ""},
		{"prod", "FinOps Bot help", "**[prod]** "},
		{"prod", "@FinOps: HELP", "**[prod]** "},
	} {
		t.Run(tt.cluster+" "+tt.text, func(t *testing.T) {
			bot, api, _ := newTestBot(t, tt.cluster)
			if err := bot.ProcessMessage(context.Background(), groupMsg(tt.text)); err != nil {
				t.Fatal(err)
			}
			reply := lastReply(t, api)["markdown"]
			if !strings.HasPrefix(reply, tt.prefix+"**CostDeck commands**") {
				t.Errorf("reply = %q", reply)
			}
			if tt.cluster != "" && (!strings.Contains(reply, "`prod list`") || !strings.Contains(reply, "start with the cluster name")) {
				t.Errorf("a named cluster's help must show its prefix: %q", reply)
			}
		})
	}

	bot, api, _ := newTestBot(t, "prod")
	if err := bot.ProcessMessage(context.Background(), groupMsg("FinOps Bot")); err != nil {
		t.Fatal(err)
	}
	if replies := api.replies(); len(replies) != 0 {
		t.Errorf("a bare mention in a shared space is not addressed to one cluster: %v", replies)
	}
}

func TestIsOwnMessageMatchesTheBotEmail(t *testing.T) {
	bot, api, _ := newTestBot(t, "")
	own := groupMsg("FinOps Bot help")
	own.PersonID, own.PersonEmail = "", "FINOPS@webex.bot"
	if err := bot.ProcessMessage(context.Background(), own); err != nil || len(api.replies()) != 0 {
		t.Errorf("a message from the bot's own address was answered: %v, %v", err, api.replies())
	}
}
