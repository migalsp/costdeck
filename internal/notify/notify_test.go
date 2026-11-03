package notify

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
	"github.com/migalsp/costdeck-operator/internal/finops"
	"github.com/migalsp/costdeck-operator/internal/netguard"
	"github.com/migalsp/costdeck-operator/internal/webex"
)

// recorder is a webhook endpoint that keeps what it receives.
type recorder struct {
	mu     sync.Mutex
	bodies []map[string]any
	status int
}

func (rec *recorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	rec.mu.Lock()
	rec.bodies = append(rec.bodies, body)
	rec.mu.Unlock()
	if rec.status != 0 {
		w.WriteHeader(rec.status)
	}
}

func (rec *recorder) count() int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return len(rec.bodies)
}

func dispatcher(t *testing.T, slackURL, teamsURL string, mails *[]Mail) *Dispatcher {
	t.Helper()
	t.Setenv("POD_NAMESPACE", "costdeck")
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = finopsv1.AddToScheme(scheme)
	cfg := &finopsv1.CostDeckConfig{Name: "default", Namespace: "costdeck", Spec: finopsv1.CostDeckConfigSpec{
		ClusterName: "prod-eu",
		Integrations: finopsv1.IntegrationsConfig{Notifications: &finopsv1.NotificationsConfig{
			Slack: &finopsv1.WebhookChannelConfig{Enabled: true, SecretRef: "slack", Events: []finopsv1.NotificationEvent{EventAlerts}},
			Teams: &finopsv1.WebhookChannelConfig{Enabled: true, SecretRef: "teams"},
			Email: &finopsv1.EmailChannelConfig{Enabled: true, Host: "smtp.example.com", From: "costdeck@example.com", To: []string{"finops@example.com"},
				Username: "costdeck", SecretRef: "smtp", Events: []finopsv1.NotificationEvent{EventDigests}},
		}},
	}}
	secret := func(name, key, value string) *corev1.Secret {
		return &corev1.Secret{Name: name, Namespace: "costdeck", Data: map[string][]byte{key: []byte(value)}}
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cfg,
		secret("slack", SecretKeyWebhookURL, slackURL), secret("teams", SecretKeyWebhookURL, teamsURL), secret("smtp", SecretKeyPassword, "s3cret")).Build()
	return &Dispatcher{Client: c, Mail: func(_ context.Context, m Mail) error { *mails = append(*mails, m); return nil }}
}

func TestSendRoutesEachEventToTheChannelsThatWantIt(t *testing.T) {
	slack, teams := &recorder{}, &recorder{}
	slackSrv, teamsSrv := httptest.NewServer(slack), httptest.NewServer(teams)
	defer slackSrv.Close()
	defer teamsSrv.Close()
	var mails []Mail
	d := dispatcher(t, slackSrv.URL, teamsSrv.URL, &mails)
	ctx := context.Background()

	for _, c := range []struct {
		event finopsv1.NotificationEvent
		want  string
	}{
		{EventSchedules, "Teams"},
		{EventAlerts, "Slack,Teams"},
		{EventDigests, "Teams,email"},
	} {
		got, err := d.Send(ctx, c.event, "**Budget shop** reached 90%")
		if err != nil || strings.Join(got, ",") != c.want {
			t.Errorf("%s: delivered %v, %v; want %s", c.event, got, err, c.want)
		}
	}
	if slack.count() != 1 || teams.count() != 3 || len(mails) != 1 {
		t.Fatalf("slack %d, teams %d, email %d", slack.count(), teams.count(), len(mails))
	}
	if text := slack.bodies[0]["text"]; text != "*[prod-eu]* *Budget shop* reached 90%" {
		t.Errorf("slack text = %q", text)
	}
	m := mails[0]
	if m.Password != "s3cret" || m.Subject != "Cost Deck [prod-eu]: Budget shop reached 90%" || !strings.HasPrefix(m.Body, "Budget shop reached 90%") {
		t.Errorf("mail = %+v", m)
	}
}

func TestSendKeepsWhatAChannelTookWhenAnotherFails(t *testing.T) {
	down, teams := &recorder{status: http.StatusNotFound}, &recorder{}
	downSrv, teamsSrv := httptest.NewServer(down), httptest.NewServer(teams)
	defer downSrv.Close()
	defer teamsSrv.Close()
	var mails []Mail
	d := dispatcher(t, downSrv.URL, teamsSrv.URL, &mails)
	got, err := d.Send(context.Background(), EventAlerts, "alert")
	if err != nil || strings.Join(got, ",") != "Teams" {
		t.Errorf("partial failure: delivered %v, %v", got, err)
	}

	teamsSrv.Close()
	got, err = d.Send(context.Background(), EventAlerts, "alert")
	if err == nil || len(got) != 0 || !strings.Contains(err.Error(), "Slack: 127.0.0.1") || !strings.Contains(err.Error(), "404") {
		t.Errorf("every channel down: delivered %v, %v", got, err)
	}
	if strings.Contains(err.Error(), downSrv.URL) {
		t.Errorf("the error repeats the webhook URL, a credential: %v", err)
	}
}

func TestSendWithNothingConfigured(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	scheme := runtime.NewScheme()
	_ = finopsv1.AddToScheme(scheme)
	d := &Dispatcher{Client: fake.NewClientBuilder().WithScheme(scheme).Build()}
	if got, err := d.Send(context.Background(), EventAlerts, "x"); err != nil || len(got) != 0 {
		t.Errorf("delivered %v, %v", got, err)
	}
}

func TestFormats(t *testing.T) {
	md := "# Weekly digest\n**Spend** went up, see [the report](https://costdeck.example.com/reports?a=1&b=2)\n\n- shop <dev>"
	if got := SlackText(md); got != "*Weekly digest*\n*Spend* went up, see <https://costdeck.example.com/reports?a=1&amp;b=2|the report>\n\n- shop &lt;dev&gt;" {
		t.Errorf("slack = %q", got)
	}
	card := TeamsCard(md)
	body := card["attachments"].([]any)[0].(map[string]any)["content"].(map[string]any)["body"].([]any)
	if len(body) != 3 || body[0].(map[string]any)["text"] != "**Weekly digest**" || body[2].(map[string]any)["text"] != "- shop <dev>" {
		t.Errorf("teams body = %v", body)
	}
	subject, text := Email("", md)
	if subject != "Cost Deck: Weekly digest" || !strings.Contains(text, "see the report (https://costdeck.example.com/reports?a=1&b=2)") || strings.Contains(text, "**") {
		t.Errorf("email = %q / %q", subject, text)
	}
	long, _ := Email("", strings.Repeat("x", 200))
	if len([]rune(long)) != len("Cost Deck: ")+118 {
		t.Errorf("a long subject is not shortened: %d", len([]rune(long)))
	}
}

func TestCheckWebhook(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://hooks.slack.com/services/T/B/x":                 true,
		"http://hook.smoke-notify.svc:8080/cgi-bin/hook":         true,
		"ftp://example.com/x":                                    false,
		"https://":                                               false,
		"http://169.254.169.254/latest/meta-data/":               false,
		"https://prod-12.westeurope.logic.azure.com/workflows/x": true,
	} {
		if err := CheckWebhook(raw); (err == nil) != ok {
			t.Errorf("%s: %v", raw, err)
		}
	}
	if err := CheckAddresses("Cost Deck <costdeck@example.com>", []string{"a@example.com"}); err != nil {
		t.Error(err)
	}
	if err := CheckAddresses("costdeck@example.com", []string{"not an address"}); err == nil {
		t.Error("a bad recipient was accepted")
	}
}

// smtpServer speaks just enough SMTP to take one message, without STARTTLS.
func smtpServer(t *testing.T) (addr string, received chan string) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	received = make(chan string, 1)
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		r := bufio.NewReader(conn)
		say := func(s string) { _, _ = io.WriteString(conn, s+"\r\n") }
		say("220 test ESMTP")
		var transcript strings.Builder
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			transcript.WriteString(line)
			switch cmd := strings.ToUpper(strings.TrimSpace(line)); {
			case strings.HasPrefix(cmd, "EHLO"):
				say("250-test")
				say("250 8BITMIME")
			case cmd == "DATA":
				say("354 go ahead")
				for {
					l, err := r.ReadString('\n')
					if err != nil || l == ".\r\n" {
						break
					}
					transcript.WriteString(l)
				}
				say("250 queued")
			case cmd == "QUIT":
				say("221 bye")
				received <- transcript.String()
				return
			default:
				say("250 ok")
			}
		}
	}()
	return l.Addr().String(), received
}

func TestSendMail(t *testing.T) {
	addr, received := smtpServer(t)
	host, port, _ := net.SplitHostPort(addr)
	p := 0
	for _, c := range port {
		p = p*10 + int(c-'0')
	}
	m := Mail{Host: host, Port: p, Security: "none", From: "Cost Deck <costdeck@example.com>", To: []string{"finops@example.com"},
		Subject: "Cost Deck: Budget für shop", Body: "Line one\nLine two"}
	if err := SendMail(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	got := <-received
	for _, want := range []string{"MAIL FROM:<costdeck@example.com>", "RCPT TO:<finops@example.com>", "Subject: =?utf-8?q?Cost_Deck:_Budget_f=C3=BCr_shop?=", "Line one\r\nLine two"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}

	m.Username, m.Password = "costdeck", "secret"
	if err := SendMail(context.Background(), m); err == nil || !strings.Contains(err.Error(), "without TLS") {
		t.Errorf("a password over plain SMTP: %v", err)
	}
	addr, _ = smtpServer(t)
	host, port, _ = net.SplitHostPort(addr)
	p = 0
	for _, c := range port {
		p = p*10 + int(c-'0')
	}
	if err := SendMail(context.Background(), Mail{Host: host, Port: p, From: "a@example.com", To: []string{"b@example.com"}}); err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Errorf("starttls against a server without it: %v", err)
	}
}

func TestSendMailWritesHeadersFromParsedAddresses(t *testing.T) {
	addr, received := smtpServer(t)
	host, port, _ := net.SplitHostPort(addr)
	p, _ := strconv.Atoi(port)
	m := Mail{Host: host, Port: p, Security: "none", From: "Cost Deck München <costdeck@example.com>",
		To: []string{"FinOps <finops@example.com>", "ops@example.com"}, Subject: "s", Body: "b"}
	if err := SendMail(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	got := <-received
	// Header values are the parsed addresses, encoded, never the strings as they came.
	for _, want := range []string{"From: =?utf-8?q?Cost_Deck_M=C3=BCnchen?= <costdeck@example.com>\r\n",
		"To: \"FinOps\" <finops@example.com>, <ops@example.com>\r\n", "Message-ID: <", "@example.com>\r\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}

	// An address that does not parse is refused before anything is sent: the settings
	// test sends a form that was never saved.
	for _, bad := range []Mail{
		{Host: host, Port: p, Security: "none", From: "a@example.com", To: []string{"b@example.com\r\nBcc: spy@example.net"}},
		{Host: host, Port: p, Security: "none", From: "a@example.com\r\nBcc: spy@example.net", To: []string{"b@example.com"}},
	} {
		if err := SendMail(context.Background(), bad); err == nil || !strings.Contains(err.Error(), "mail: expected single address") {
			t.Errorf("%q to %q was not refused as an address: %v", bad.From, bad.To, err)
		}
	}
}

func TestReady(t *testing.T) {
	cfg := &finopsv1.CostDeckConfig{Spec: finopsv1.CostDeckConfigSpec{Integrations: finopsv1.IntegrationsConfig{
		Messenger: &finopsv1.MessengerIntegrationConfig{Webex: &finopsv1.WebexConfig{Enabled: true, RoomID: "space"}},
		Notifications: &finopsv1.NotificationsConfig{
			Slack: &finopsv1.WebhookChannelConfig{Enabled: true, SecretRef: "s", Events: []finopsv1.NotificationEvent{EventSchedules}},
			Teams: &finopsv1.WebhookChannelConfig{Enabled: true}, // no webhook yet
			Email: &finopsv1.EmailChannelConfig{Enabled: true, Host: "smtp", To: []string{"a@example.com"}},
		},
	}}}
	for event, want := range map[finopsv1.NotificationEvent]string{
		EventSchedules: "Slack,email", // Webex only announces with notifyTransitions
		EventAlerts:    "Webex,email",
		EventDigests:   "Webex,email",
	} {
		if got := strings.Join(Ready(cfg, event), ","); got != want {
			t.Errorf("%s: %s, want %s", event, got, want)
		}
	}
}

type capturedLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *capturedLog) into(ctx context.Context) context.Context {
	return logf.IntoContext(ctx, funcr.New(func(prefix, args string) {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.lines = append(l.lines, prefix+" "+args)
	}, funcr.Options{}))
}

func (l *capturedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func configClient(t *testing.T, spec finopsv1.CostDeckConfigSpec, secrets ...*corev1.Secret) *fake.ClientBuilder {
	t.Helper()
	t.Setenv("POD_NAMESPACE", "costdeck")
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = finopsv1.AddToScheme(scheme)
	b := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&finopsv1.CostDeckConfig{Name: "default", Namespace: "costdeck", Spec: spec})
	for _, s := range secrets {
		b = b.WithObjects(s)
	}
	return b
}

func teamsWebhook(url string) *corev1.Secret {
	return &corev1.Secret{Name: "teams", Namespace: "costdeck", Data: map[string][]byte{SecretKeyWebhookURL: []byte(url)}}
}

func teamsOnly(events ...finopsv1.NotificationEvent) finopsv1.CostDeckConfigSpec {
	return finopsv1.CostDeckConfigSpec{Integrations: finopsv1.IntegrationsConfig{Notifications: &finopsv1.NotificationsConfig{
		Teams: &finopsv1.WebhookChannelConfig{Enabled: true, SecretRef: "teams", Events: events},
	}}}
}

func TestNotifySendsScheduleChangesAndOnlyLogsFailures(t *testing.T) {
	teams := &recorder{}
	srv := httptest.NewServer(teams)
	defer srv.Close()
	d := &Dispatcher{Client: configClient(t, teamsOnly(EventSchedules), teamsWebhook(srv.URL)).Build()}
	logs := &capturedLog{}
	ctx := logs.into(context.Background())

	d.Notify(ctx, "Scaled **dev** down")
	if teams.count() != 1 {
		t.Fatalf("Teams got %d message(s), want the schedule change", teams.count())
	}
	if strings.Contains(logs.String(), "Could not send") {
		t.Errorf("a delivered notification logged a failure: %s", logs)
	}

	srv.Close()
	d.Notify(ctx, "Scaled **dev** up")
	if !strings.Contains(logs.String(), "Could not send a scaling notification") || !strings.Contains(logs.String(), "Teams:") {
		t.Errorf("a failed scaling notification is not logged with its channel: %s", logs)
	}
}

func TestPostSaysWhereAMessageWent(t *testing.T) {
	slack, teams := &recorder{}, &recorder{}
	slackSrv, teamsSrv := httptest.NewServer(slack), httptest.NewServer(teams)
	defer slackSrv.Close()
	defer teamsSrv.Close()
	var mails []Mail
	d := dispatcher(t, slackSrv.URL, teamsSrv.URL, &mails)
	ctx := context.Background()

	if got, err := (Poster{Dispatcher: d, Event: EventAlerts}).Post(ctx, "alert"); err != nil || got != "Slack, Teams" {
		t.Errorf("Post(alerts) = %q, %v; want the channels joined", got, err)
	}

	quiet := &Dispatcher{Client: configClient(t, teamsOnly(EventSchedules), teamsWebhook(teamsSrv.URL)).Build()}
	got, err := (Poster{Dispatcher: quiet, Event: EventDigests}).Post(ctx, "digest")
	if got != "" || !errors.Is(err, finops.ErrNoDestination) {
		t.Errorf("Post with no channel for digests = %q, %v; want finops.ErrNoDestination", got, err)
	}

	teamsSrv.Close()
	down := &Dispatcher{Client: configClient(t, teamsOnly(), teamsWebhook(teamsSrv.URL)).Build()}
	got, err = (Poster{Dispatcher: down, Event: EventAlerts}).Post(ctx, "alert")
	if got != "" || err == nil || errors.Is(err, finops.ErrNoDestination) || !strings.Contains(err.Error(), "Teams:") {
		t.Errorf("Post with every channel down = %q, %v; want the delivery error, not a missing destination", got, err)
	}
}

func TestSendReportsChannelsWithoutUsableSecrets(t *testing.T) {
	spec := finopsv1.CostDeckConfigSpec{Integrations: finopsv1.IntegrationsConfig{Notifications: &finopsv1.NotificationsConfig{
		Slack: &finopsv1.WebhookChannelConfig{Enabled: true},
		Teams: &finopsv1.WebhookChannelConfig{Enabled: true, SecretRef: "missing"},
		Email: &finopsv1.EmailChannelConfig{Enabled: true, Host: "smtp.example.com", From: "a@example.com", To: []string{"b@example.com"}, Username: "costdeck", SecretRef: "smtp"},
	}}}
	empty := &corev1.Secret{Name: "smtp", Namespace: "costdeck", Data: map[string][]byte{SecretKeyPassword: []byte("  \n")}}
	mailed := 0
	d := &Dispatcher{Client: configClient(t, spec, empty).Build(), Mail: func(context.Context, Mail) error { mailed++; return nil }}

	got, err := d.Send(context.Background(), EventAlerts, "alert")
	if len(got) != 0 || err == nil || mailed != 0 {
		t.Fatalf("Send = %v, %v, %d mail(s); want every channel to fail before sending", got, err, mailed)
	}
	for _, want := range []string{"Slack: no Secret holds the webhook url", "Teams: read Secret missing", "email: the Secret smtp has no PASSWORD"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
}

func TestSendPostsToWebex(t *testing.T) {
	var mu sync.Mutex
	var messages []map[string]string
	webexAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		messages = append(messages, body)
		mu.Unlock()
		_, _ = io.WriteString(w, `{"id":"m1"}`)
	}))
	defer webexAPI.Close()
	webexMessages := func() []map[string]string {
		mu.Lock()
		defer mu.Unlock()
		return append([]map[string]string(nil), messages...)
	}
	teams := &recorder{}
	teamsSrv := httptest.NewServer(teams)
	defer teamsSrv.Close()

	withWebex := func(wx *finopsv1.WebexConfig) *Dispatcher {
		spec := teamsOnly()
		spec.ClusterName = "prod-eu"
		spec.Integrations.Messenger = &finopsv1.MessengerIntegrationConfig{Webex: wx}
		token := &corev1.Secret{Name: "webex", Namespace: "costdeck", Data: map[string][]byte{webex.SecretKeyBotToken: []byte("bot-token")}}
		c := configClient(t, spec, teamsWebhook(teamsSrv.URL), token).Build()
		return &Dispatcher{Client: c, Webex: &webex.Notifier{Client: c, NewClient: func(tok string) *webex.Client {
			return &webex.Client{Token: tok, BaseURL: webexAPI.URL, HTTP: webexAPI.Client()}
		}}}
	}
	ctx := context.Background()

	d := withWebex(&finopsv1.WebexConfig{Enabled: true, RoomID: "space", SecretRef: "webex"})
	if got, err := d.Send(ctx, EventAlerts, "Budget reached"); err != nil || strings.Join(got, ",") != "Webex,Teams" {
		t.Fatalf("alerts = %v, %v; want Webex and Teams", got, err)
	}
	if got := webexMessages(); len(got) != 1 || got[0]["roomId"] != "space" || got[0]["markdown"] != "**[prod-eu]** Budget reached" {
		t.Errorf("Webex got %v", got)
	}
	if got, err := d.Send(ctx, EventSchedules, "Scaled dev down"); err != nil || strings.Join(got, ",") != ChannelTeams || len(webexMessages()) != 1 {
		t.Errorf("schedules without notifyTransitions = %v, %v, %d Webex message(s); want Teams only", got, err, len(webexMessages()))
	}

	transitions := withWebex(&finopsv1.WebexConfig{Enabled: true, RoomID: "space", SecretRef: "webex", NotifyTransitions: true})
	if got, err := transitions.Send(ctx, EventSchedules, "Scaled dev up"); err != nil || strings.Join(got, ",") != "Webex,Teams" || len(webexMessages()) != 2 {
		t.Errorf("schedules with notifyTransitions = %v, %v; want Webex and Teams", got, err)
	}

	noSpace := withWebex(&finopsv1.WebexConfig{Enabled: true, SecretRef: "webex"})
	if got, err := noSpace.Send(ctx, EventAlerts, "x"); err != nil || strings.Join(got, ",") != ChannelTeams {
		t.Errorf("Webex without a space = %v, %v; want it skipped quietly", got, err)
	}

	noToken := withWebex(&finopsv1.WebexConfig{Enabled: true, RoomID: "space", SecretRef: "no-such-secret"})
	if got, err := noToken.Send(ctx, EventAlerts, "x"); err != nil || strings.Join(got, ",") != ChannelTeams {
		t.Errorf("a broken Webex = %v, %v; want Teams to keep the message and no error", got, err)
	}
	teamsSrv.Close()
	if _, err := noToken.Send(ctx, EventAlerts, "x"); err == nil || !strings.Contains(err.Error(), "Webex: read secret costdeck/no-such-secret") {
		t.Errorf("every channel down = %v; want the Webex failure named", err)
	}
}

func TestHTTPClientRefusesMetadataAddressesByDefault(t *testing.T) {
	own := &http.Client{}
	if (&Dispatcher{HTTP: own}).HTTPClient() != own {
		t.Error("a client set by the caller is not used")
	}
	hc := (&Dispatcher{}).HTTPClient()
	transport, ok := hc.Transport.(*http.Transport)
	if !ok || hc.Timeout != 15*time.Second {
		t.Fatalf("default client = %+v; want a 15s timeout and an http.Transport", hc)
	}
	if _, err := transport.DialContext(context.Background(), "tcp", "169.254.169.254:80"); !errors.Is(err, netguard.ErrForbiddenAddress) {
		t.Errorf("the default webhook client dialled a metadata address: %v", err)
	}
}

func TestSendMailDialsThroughTheGuard(t *testing.T) {
	for _, tc := range []struct {
		security string
		port     int
		wantAddr string
	}{
		{"", 0, "169.254.169.254:587"},
		{"starttls", 0, "169.254.169.254:587"},
		{"tls", 0, "169.254.169.254:465"},
		{"none", 2525, "169.254.169.254:2525"},
	} {
		m := Mail{Host: "169.254.169.254", Port: tc.port, Security: tc.security, From: "a@example.com", To: []string{"b@example.com"}}
		err := SendMail(context.Background(), m)
		if !errors.Is(err, netguard.ErrForbiddenAddress) || !strings.Contains(err.Error(), tc.wantAddr) {
			t.Errorf("security %q, port %d: %v; want %s refused as a metadata address", tc.security, tc.port, err, tc.wantAddr)
		}
	}
}

func TestSendMailRefusesIncompleteMail(t *testing.T) {
	for name, m := range map[string]Mail{
		"no server":    {From: "a@example.com", To: []string{"b@example.com"}},
		"no sender":    {Host: "smtp.example.com", To: []string{"b@example.com"}},
		"no recipient": {Host: "smtp.example.com", From: "a@example.com"},
	} {
		if err := SendMail(context.Background(), m); err == nil || !strings.Contains(err.Error(), "needs a server, a sender and at least one recipient") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestSendMailWithImplicitTLSNeverSpeaksPlainSMTP(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	firstByte := make(chan byte, 1)
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		b := make([]byte, 1)
		if _, err := io.ReadFull(conn, b); err == nil {
			firstByte <- b[0]
		}
	}()
	host, port, _ := net.SplitHostPort(l.Addr().String())
	p, _ := strconv.Atoi(port)
	err = SendMail(context.Background(), Mail{Host: host, Port: p, Security: "tls", Username: "costdeck", Password: "secret",
		From: "a@example.com", To: []string{"b@example.com"}})
	if err == nil {
		t.Fatal("a server that never completes a TLS handshake took the mail")
	}
	select {
	case b := <-firstByte:
		if b != 0x16 {
			t.Errorf("the first byte sent was %#x, want a TLS handshake record (0x16)", b)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the client sent nothing")
	}
}
