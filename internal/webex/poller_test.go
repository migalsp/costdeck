package webex

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

type requestLog struct {
	mu             sync.Mutex
	inner          http.Handler
	requests       []string
	auth           []string
	failMessagesIn string
}

func (l *requestLog) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	l.mu.Lock()
	entry := r.Method + " " + r.URL.Path
	if r.URL.RawQuery != "" {
		entry += "?" + r.URL.RawQuery
	}
	l.requests = append(l.requests, entry)
	l.auth = append(l.auth, r.Header.Get("Authorization"))
	failRoom := l.failMessagesIn
	l.mu.Unlock()
	if failRoom != "" && r.Method == http.MethodGet && r.URL.Path == pathMessages && r.URL.Query().Get("roomId") == failRoom {
		http.Error(w, `{"message":"Bots cannot read this space."}`, http.StatusForbidden)
		return
	}
	l.inner.ServeHTTP(w, r)
}

func (l *requestLog) count(prefix string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, r := range l.requests {
		if strings.HasPrefix(r, prefix) {
			n++
		}
	}
	return n
}

func (l *requestLog) matching(prefix string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, r := range l.requests {
		if strings.HasPrefix(r, prefix) {
			out = append(out, r)
		}
	}
	return out
}

func webexEnv(t *testing.T, wx *finopsv1.WebexConfig, clusterName string, secret map[string][]byte) (*Poller, *fakeWebex, *requestLog, client.Client) {
	t.Helper()
	t.Setenv("POD_NAMESPACE", "costdeck")
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(finopsv1.AddToScheme(scheme))
	cfg := &finopsv1.CostDeckConfig{Name: "default", Namespace: "costdeck"}
	cfg.Spec.ClusterName = clusterName
	if wx != nil {
		cfg.Spec.Integrations.Messenger = &finopsv1.MessengerIntegrationConfig{Webex: wx}
	}
	objs := []client.Object{cfg}
	if secret != nil {
		objs = append(objs, &corev1.Secret{Name: "wx", Namespace: "costdeck", Data: secret})
	}
	k8s := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()

	api := &fakeWebex{me: Person{ID: "bot-id", DisplayName: "FinOps Bot", Emails: []string{"finops@webex.bot"}}, messages: map[string][]Message{}}
	log := &requestLog{inner: api}
	srv := httptest.NewServer(log)
	t.Cleanup(srv.Close)
	p := &Poller{Client: k8s, NewClient: func(token string) *Client { return &Client{Token: token, BaseURL: srv.URL, HTTP: srv.Client()} }}
	return p, api, log, k8s
}

func addMessage(api *fakeWebex, room string, m Message) {
	api.mu.Lock()
	defer api.mu.Unlock()
	m.RoomID = room
	api.messages[room] = append(api.messages[room], m)
}

func TestLoadSettings(t *testing.T) {
	full := map[string][]byte{SecretKeyBotToken: []byte("bot"), SecretKeyWebhookSecret: []byte("hook")}
	tests := []struct {
		name    string
		wx      *finopsv1.WebexConfig
		secret  map[string][]byte
		want    *Settings
		wantErr string
	}{
		{name: "no messenger section"},
		{name: "switched off", wx: &finopsv1.WebexConfig{SecretRef: "wx", RoomID: "room"}, secret: full},
		{name: "enabled", wx: &finopsv1.WebexConfig{Enabled: true, SecretRef: "wx", RoomID: "room"}, secret: full, want: &Settings{Token: "bot", WebhookSecret: "hook", RoomID: "room", ClusterName: "prod"}},
		{name: "no Secret named", wx: &finopsv1.WebexConfig{Enabled: true}, wantErr: "no secret configured"},
		{name: "Secret missing", wx: &finopsv1.WebexConfig{Enabled: true, SecretRef: "wx"}, wantErr: "read secret costdeck/wx"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, k8s := webexEnv(t, tt.wx, "prod", tt.secret)
			got, err := LoadSettings(context.Background(), k8s)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("LoadSettings() error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || (got == nil) != (tt.want == nil) || (got != nil && *got != *tt.want) {
				t.Errorf("LoadSettings() = %+v, %v; want %+v", got, err, tt.want)
			}
		})
	}
}

func TestPollerStandsDown(t *testing.T) {
	tests := []struct {
		name   string
		wx     *finopsv1.WebexConfig
		secret map[string][]byte
	}{
		{name: "Webex switched off", wx: &finopsv1.WebexConfig{SecretRef: "wx"}, secret: map[string][]byte{SecretKeyBotToken: []byte("t")}},
		{name: "webhooks deliver the messages", wx: &finopsv1.WebexConfig{Enabled: true, SecretRef: "wx"}, secret: map[string][]byte{SecretKeyBotToken: []byte("t"), SecretKeyWebhookSecret: []byte("hook")}},
		{name: "no bot token", wx: &finopsv1.WebexConfig{Enabled: true, SecretRef: "wx"}, secret: map[string][]byte{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, _, log, _ := webexEnv(t, tt.wx, "", tt.secret)
			p.Poll(context.Background())
			if len(log.requests) != 0 {
				t.Errorf("requests = %v, want none", log.requests)
			}
		})
	}
}

func TestPollerCachesIdentityAndRoomsUntilTheTokenChanges(t *testing.T) {
	p, api, log, k8s := webexEnv(t, &finopsv1.WebexConfig{Enabled: true, SecretRef: "wx"}, "", map[string][]byte{SecretKeyBotToken: []byte("t1")})
	api.rooms = []Room{{ID: "room", Title: "Ops", Type: "group"}}
	ctx := context.Background()
	for range 3 {
		p.Poll(ctx)
	}
	if me, rooms, msgs := log.count("GET /people/me"), log.count("GET /rooms"), log.count("GET /messages"); me != 1 || rooms != 1 || msgs != 3 {
		t.Errorf("three rounds asked /people/me %d, /rooms %d and /messages %d times; want 1, 1 and 3", me, rooms, msgs)
	}

	secret := &corev1.Secret{}
	if err := k8s.Get(ctx, client.ObjectKey{Name: "wx", Namespace: "costdeck"}, secret); err != nil {
		t.Fatal(err)
	}
	secret.Data[SecretKeyBotToken] = []byte("t2")
	if err := k8s.Update(ctx, secret); err != nil {
		t.Fatal(err)
	}
	p.Poll(ctx)
	if me, rooms := log.count("GET /people/me"), log.count("GET /rooms"); me != 2 || rooms != 2 {
		t.Errorf("after a new token /people/me was asked %d and /rooms %d times; both must be asked again", me, rooms)
	}
	if last := log.auth[len(log.auth)-1]; last != "Bearer t2" {
		t.Errorf("last request used %q", last)
	}
}

func TestPollerPollsOnlyTheConfiguredSpace(t *testing.T) {
	p, api, log, _ := webexEnv(t, &finopsv1.WebexConfig{Enabled: true, SecretRef: "wx", RoomID: "ops"}, "", map[string][]byte{SecretKeyBotToken: []byte("t")})
	api.rooms = []Room{{ID: "ops", Title: "Ops", Type: "group"}, {ID: "other", Title: "Other", Type: "group"}}
	p.Poll(context.Background())
	if n := log.count("GET /rooms?"); n != 0 {
		t.Errorf("listed every room %d times with a space configured", n)
	}
	if n := log.count("GET /rooms/ops"); n != 1 {
		t.Errorf("GET /rooms/ops asked %d times", n)
	}
	msgs := log.matching("GET /messages")
	if len(msgs) != 1 || !strings.Contains(msgs[0], "roomId=ops") || !strings.Contains(msgs[0], "mentionedPeople=me") {
		t.Errorf("message listings = %v, want only the configured group space, mentions only", msgs)
	}

	missing, _, missingLog, _ := webexEnv(t, &finopsv1.WebexConfig{Enabled: true, SecretRef: "wx", RoomID: "gone"}, "", map[string][]byte{SecretKeyBotToken: []byte("t")})
	missing.Poll(context.Background())
	if n := missingLog.count("GET /messages"); n != 0 {
		t.Errorf("a space the bot cannot see was polled %d times", n)
	}
}

func TestPollerReadsEveryMessageOfADirectSpace(t *testing.T) {
	p, api, log, _ := webexEnv(t, &finopsv1.WebexConfig{Enabled: true, SecretRef: "wx"}, "prod", map[string][]byte{SecretKeyBotToken: []byte("t")})
	api.rooms = []Room{{ID: "dm", Title: "Alice", Type: "direct"}, {ID: "grp", Title: "Ops", Type: "group"}}
	ctx := context.Background()
	p.Poll(ctx)
	for _, q := range log.matching("GET /messages") {
		group := strings.Contains(q, "roomId=grp")
		if group != strings.Contains(q, "mentionedPeople=me") {
			t.Errorf("listing %q: bots may read all of a direct space but only mentions in a group space", q)
		}
	}

	addMessage(api, "dm", Message{ID: "d1", Text: "what's up", PersonID: "alice", Created: time.Now().Add(time.Second)})
	addMessage(api, "grp", Message{ID: "g1", Text: "FinOps Bot what's up", PersonID: "bob", Created: time.Now().Add(time.Second)})
	p.Poll(ctx)
	replies := api.replies()
	if len(replies) != 1 || replies[0]["roomId"] != "dm" || !strings.Contains(replies[0]["markdown"], "did not understand") {
		t.Errorf("replies = %v; a direct message is addressed to this cluster, a group message without its name is not", replies)
	}
}

func TestPollerKeepsPollingWhenOneSpaceFails(t *testing.T) {
	p, api, log, _ := webexEnv(t, &finopsv1.WebexConfig{Enabled: true, SecretRef: "wx"}, "", map[string][]byte{SecretKeyBotToken: []byte("t")})
	api.rooms = []Room{{ID: "locked", Title: "Locked", Type: "group"}, {ID: "ops", Title: "Ops", Type: "group"}}
	log.failMessagesIn = "locked"
	ctx := context.Background()
	p.Poll(ctx)
	addMessage(api, "ops", Message{ID: "m1", Text: "FinOps Bot help", PersonID: "u1", Created: time.Now().Add(time.Second)})
	p.Poll(ctx)
	if replies := api.replies(); len(replies) != 1 || replies[0]["roomId"] != "ops" {
		t.Errorf("replies = %v; a space the bot cannot read must not stop the others", replies)
	}
}

func TestPollerLogsARepeatedErrorOnce(t *testing.T) {
	p, api, _, k8s := webexEnv(t, &finopsv1.WebexConfig{Enabled: true, SecretRef: "wx"}, "", nil)
	api.rooms = []Room{{ID: "ops", Title: "Ops", Type: "group"}}
	var mu sync.Mutex
	var errorsLogged []string
	ctx := logf.IntoContext(context.Background(), funcr.New(func(_, args string) {
		if strings.Contains(args, `"error"=`) {
			mu.Lock()
			errorsLogged = append(errorsLogged, args)
			mu.Unlock()
		}
	}, funcr.Options{}))
	logged := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(errorsLogged)
	}

	p.Poll(ctx)
	p.Poll(ctx)
	if n := logged(); n != 1 || !strings.Contains(errorsLogged[0], "Could not load Webex settings") {
		t.Fatalf("errors logged = %v; the same failure every round is logged once", errorsLogged)
	}

	secret := &corev1.Secret{Name: "wx", Namespace: "costdeck", Data: map[string][]byte{SecretKeyBotToken: []byte("t")}}
	if err := k8s.Create(ctx, secret); err != nil {
		t.Fatal(err)
	}
	p.Poll(ctx)
	if n := logged(); n != 1 {
		t.Fatalf("a successful round logged an error: %v", errorsLogged)
	}
	if err := k8s.Delete(ctx, secret); err != nil {
		t.Fatal(err)
	}
	p.Poll(ctx)
	if n := logged(); n != 2 {
		t.Errorf("errors logged = %d; a failure that returns after a good round is logged again", n)
	}
}

func TestPollerStartPollsUntilCancelled(t *testing.T) {
	p, api, log, _ := webexEnv(t, &finopsv1.WebexConfig{Enabled: true, SecretRef: "wx"}, "", map[string][]byte{SecretKeyBotToken: []byte("t")})
	api.rooms = []Room{{ID: "ops", Title: "Ops", Type: "group"}}
	p.Interval = 5 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Start(ctx) }()

	deadline := time.Now().Add(5 * time.Second)
	for log.count("GET /messages") < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := log.count("GET /messages"); n < 2 {
		t.Errorf("Start polled %d times in 5s at a 5ms interval", n)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Start() = %v, want nil on shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after the context was cancelled")
	}
}

func TestPollerFresh(t *testing.T) {
	p := &Poller{}
	now := time.Now()
	old := Message{ID: "old", Created: now.Add(-time.Minute)}
	if got := p.fresh("room", []Message{old}); got != nil {
		t.Fatalf("first round = %v; it only records a watermark", got)
	}
	m1 := Message{ID: "m1", Created: now.Add(time.Second)}
	m2 := Message{ID: "m2", Created: now.Add(2 * time.Second)}
	got := p.fresh("room", []Message{m2, old, m1})
	if len(got) != 2 || got[0].ID != "m1" || got[1].ID != "m2" {
		t.Fatalf("second round = %v, want m1 then m2", got)
	}
	if again := p.fresh("room", []Message{m2, m1, old}); again != nil {
		t.Errorf("third round = %v; seen messages must not be handled twice", again)
	}
	if other := p.fresh("other", []Message{m2}); other != nil {
		t.Errorf("first round in another room = %v; each room has its own watermark", other)
	}

	ahead := &Poller{}
	future := Message{ID: "future", Created: now.Add(time.Hour)}
	ahead.fresh("room", []Message{future})
	if got := ahead.fresh("room", []Message{future, {ID: "between", Created: now.Add(time.Minute)}}); got != nil {
		t.Errorf("got %v; the watermark is the newest message when it is ahead of the local clock", got)
	}
}

func TestCheck(t *testing.T) {
	api := &recordingWebex{reply: func(r *http.Request) (int, string) {
		switch r.URL.Path {
		case pathMe:
			if r.Header.Get("Authorization") == "Bearer expired" {
				return http.StatusUnauthorized, `{"message":"The request requires a valid access token"}`
			}
			return http.StatusOK, `{"id":"bot","displayName":"FinOps Bot"}`
		case "/rooms/ops":
			return http.StatusOK, `{"id":"ops","title":"Ops","type":"group"}`
		}
		return http.StatusNotFound, `{"message":"The requested resource could not be found."}`
	}}
	srv := httptest.NewServer(api)
	defer srv.Close()
	tests := []struct {
		name     string
		settings Settings
		want     string
		wantErr  string
	}{
		{name: "no token", settings: Settings{}, wantErr: "the Webex credentials Secret has no BOT_TOKEN"},
		{name: "expired token", settings: Settings{Token: "expired"}, wantErr: "webex API returned HTTP 401: The request requires a valid access token (the bot token is invalid or expired)"},
		{name: "polling every space", settings: Settings{Token: "t"}, want: "Connected as FinOps Bot · polling all spaces the bot is in"},
		{name: "polling one space", settings: Settings{Token: "t", RoomID: "ops"}, want: `Connected as FinOps Bot · polling space "Ops"`},
		{name: "webhooks to one space", settings: Settings{Token: "t", RoomID: "ops", WebhookSecret: "h"}, want: "Connected as FinOps Bot · signed webhooks"},
		{name: "webhooks", settings: Settings{Token: "t", WebhookSecret: "h"}, want: "Connected as FinOps Bot · signed webhooks"},
		{name: "space out of reach", settings: Settings{Token: "t", RoomID: "nope"}, wantErr: `bot "FinOps Bot" cannot access space nope: webex API returned HTTP 404`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api.mu.Lock()
			api.calls = nil
			api.mu.Unlock()
			got, err := Check(context.Background(), &Client{Token: tt.settings.Token, BaseURL: srv.URL, HTTP: srv.Client()}, &tt.settings)
			if tt.wantErr != "" {
				if got != "" || err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("Check() = %q, %v; want %q", got, err, tt.wantErr)
				}
			} else if err != nil || got != tt.want {
				t.Errorf("Check() = %q, %v; want %q", got, err, tt.want)
			}
			if tt.settings.Token == "" && len(api.calls) != 0 {
				t.Errorf("Check() without a token called Webex: %+v", api.calls)
			}
		})
	}
}
