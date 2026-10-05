package webex

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/migalsp/costdeck-operator/internal/config"
)

// Notifier posts scaling transitions to the configured Webex space.
type Notifier struct {
	Client client.Reader
	// NewClient builds the Webex API client; tests point it at a fake server.
	NewClient func(token string) *Client
}

// Notify posts a markdown message when transition notifications are enabled. It is a
// no-op otherwise, and failures are logged rather than returned: a notification must
// never hold up scaling.
func (n *Notifier) Notify(ctx context.Context, markdown string) {
	log := logf.FromContext(ctx).WithName("webex-notifier")
	cfg, err := config.Get(ctx, n.Client)
	if err != nil {
		return
	}
	wx := cfg.Spec.Integrations.Messenger
	if wx == nil || wx.Webex == nil || !wx.Webex.Enabled || !wx.Webex.NotifyTransitions || wx.Webex.RoomID == "" {
		return
	}
	settings, err := LoadSettings(ctx, n.Client)
	if err != nil || settings == nil || settings.Token == "" {
		log.Info("Could not send a Webex notification: no bot token", "error", err)
		return
	}
	api := NewClient(settings.Token)
	if n.NewClient != nil {
		api = n.NewClient(settings.Token)
	}
	if settings.ClusterName != "" {
		markdown = "**[" + settings.ClusterName + "]** " + markdown
	}
	if err := api.Send(ctx, settings.RoomID, "", markdown); err != nil {
		log.Error(err, "Could not send a Webex notification", "room", settings.RoomID)
	}
}
