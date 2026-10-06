package webex

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/migalsp/costdeck-operator/internal/config"
)

// Secret keys of the Webex credentials Secret.
const (
	SecretKeyBotToken = "BOT_TOKEN"
	// SecretKeyWebhookSecret switches delivery from polling to webhooks. Webex signs every
	// webhook with it (X-Spark-Signature), and the poller stands down so that messages are
	// not handled twice.
	SecretKeyWebhookSecret = "WEBHOOK_SECRET"
)

// Settings is the resolved Webex configuration.
type Settings struct {
	Token         string
	WebhookSecret string
	RoomID        string
	ClusterName   string
}

// LoadSettings reads the Webex configuration and credentials. It returns (nil, nil) when
// the integration is disabled.
func LoadSettings(ctx context.Context, c client.Reader) (*Settings, error) {
	cfg, err := config.Get(ctx, c)
	if err != nil {
		return nil, err
	}
	m := cfg.Spec.Integrations.Messenger
	if m == nil || m.Webex == nil || !m.Webex.Enabled {
		return nil, nil
	}
	data, err := config.SecretData(ctx, c, m.Webex.SecretRef)
	if err != nil {
		return nil, err
	}
	return &Settings{
		Token:         string(data[SecretKeyBotToken]),
		WebhookSecret: string(data[SecretKeyWebhookSecret]),
		RoomID:        m.Webex.RoomID,
		ClusterName:   cfg.Spec.ClusterName,
	}, nil
}

// Poller periodically fetches new messages addressed to the bot. It runs on the elected
// leader only, so a multi-replica deployment answers each message once.
type Poller struct {
	Client   client.Client
	Interval time.Duration
	// NewClient builds the Webex API client; tests point it at a fake server.
	NewClient func(token string) *Client

	mu        sync.Mutex
	token     string
	me        *Person
	rooms     []Room
	roomsAt   time.Time
	lastSeen  map[string]time.Time
	lastError string
}

// Start implements manager.Runnable.
func (p *Poller) Start(ctx context.Context) error {
	log := logf.FromContext(ctx).WithName("webex-poller")
	log.Info("Starting Webex poller")
	interval := p.Interval
	if interval == 0 {
		interval = 10 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			p.Poll(ctx)
		}
	}
}

// Poll runs one polling round.
func (p *Poller) Poll(ctx context.Context) {
	log := logf.FromContext(ctx).WithName("webex-poller")
	settings, err := LoadSettings(ctx, p.Client)
	if err != nil {
		p.logOnce(log, err, "Could not load Webex settings")
		return
	}
	if settings == nil || settings.Token == "" || settings.WebhookSecret != "" {
		p.reset("")
		return
	}
	p.reset(settings.Token)

	api := p.client(settings.Token)
	me, err := p.identity(ctx, api)
	if err != nil {
		p.logOnce(log, err, "Could not identify the Webex bot")
		return
	}
	rooms, err := p.roomsToPoll(ctx, api, settings.RoomID)
	if err != nil {
		p.logOnce(log, err, "Could not list Webex rooms")
		return
	}

	bot := &Bot{API: api, K8s: p.Client, Namespace: config.OperatorNamespace(), ClusterName: settings.ClusterName, SpaceID: settings.RoomID, Me: me, Background: ctx}
	for _, room := range rooms {
		msgs, err := api.Messages(ctx, room.ID, room.Type != "direct", 20)
		if err != nil {
			p.logOnce(log, err, "Could not list Webex messages", "room", room.ID)
			continue
		}
		for _, m := range p.fresh(room.ID, msgs) {
			msg := m
			if msg.RoomType == "" {
				msg.RoomType = room.Type
			}
			if err := bot.ProcessMessage(ctx, &msg); err != nil {
				log.Error(err, "Could not process Webex message", "messageId", msg.ID)
			}
		}
	}
	p.mu.Lock()
	p.lastError = ""
	p.mu.Unlock()
}

func (p *Poller) client(token string) *Client {
	if p.NewClient != nil {
		return p.NewClient(token)
	}
	return NewClient(token)
}

// reset drops cached state when the token changes or the integration is switched off.
func (p *Poller) reset(token string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.token == token {
		return
	}
	p.token, p.me, p.rooms, p.roomsAt, p.lastSeen = token, nil, nil, time.Time{}, map[string]time.Time{}
}

func (p *Poller) identity(ctx context.Context, api *Client) (*Person, error) {
	p.mu.Lock()
	me := p.me
	p.mu.Unlock()
	if me != nil {
		return me, nil
	}
	me, err := api.Me(ctx)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.me = me
	p.mu.Unlock()
	return me, nil
}

// roomsToPoll returns the configured room, or every room the bot belongs to when none is
// configured (refreshed every five minutes).
func (p *Poller) roomsToPoll(ctx context.Context, api *Client, roomID string) ([]Room, error) {
	p.mu.Lock()
	cached, fresh := p.rooms, time.Since(p.roomsAt) < 5*time.Minute
	p.mu.Unlock()
	if cached != nil && fresh {
		return cached, nil
	}

	var rooms []Room
	if roomID != "" {
		room, err := api.Room(ctx, roomID)
		if err != nil {
			return nil, err
		}
		rooms = []Room{*room}
	} else {
		var err error
		if rooms, err = api.Rooms(ctx, 50); err != nil {
			return nil, err
		}
	}
	p.mu.Lock()
	p.rooms, p.roomsAt = rooms, time.Now()
	p.mu.Unlock()
	return rooms, nil
}

// fresh returns the messages created after the last one seen in the room, oldest first.
// The first round in a room only records a watermark, so history is never replayed.
func (p *Poller) fresh(roomID string, msgs []Message) []Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.lastSeen == nil {
		p.lastSeen = map[string]time.Time{}
	}
	sort.Slice(msgs, func(i, j int) bool { return msgs[i].Created.Before(msgs[j].Created) })
	watermark, seen := p.lastSeen[roomID]
	if !seen {
		watermark = time.Now()
		if len(msgs) > 0 && msgs[len(msgs)-1].Created.After(watermark) {
			watermark = msgs[len(msgs)-1].Created
		}
		p.lastSeen[roomID] = watermark
		return nil
	}
	var out []Message
	for _, m := range msgs {
		if m.Created.After(watermark) {
			out = append(out, m)
			p.lastSeen[roomID] = m.Created
		}
	}
	return out
}

// logOnce logs a polling error once until it changes, instead of every ten seconds.
func (p *Poller) logOnce(log logr.Logger, err error, msg string, kv ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.lastError == err.Error() {
		return
	}
	p.lastError = err.Error()
	log.Error(err, msg, kv...)
}

// Check validates Webex settings and describes the result, e.g.
// "Connected as CostDeck Bot · polling space Ops".
func Check(ctx context.Context, api *Client, s *Settings) (string, error) {
	if s.Token == "" {
		return "", fmt.Errorf("the Webex credentials Secret has no %s", SecretKeyBotToken)
	}
	me, err := api.Me(ctx)
	if err != nil {
		return "", err
	}
	delivery := "polling all spaces the bot is in"
	if s.WebhookSecret != "" {
		delivery = "signed webhooks"
	}
	if s.RoomID != "" {
		room, err := api.Room(ctx, s.RoomID)
		if err != nil {
			return "", fmt.Errorf("bot %q cannot access space %s: %w", me.DisplayName, s.RoomID, err)
		}
		if s.WebhookSecret == "" {
			delivery = fmt.Sprintf("polling space %q", room.Title)
		}
	}
	return fmt.Sprintf("Connected as %s · %s", me.DisplayName, delivery), nil
}
