package webex

import (
	"context"
	"errors"

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
	cfg, err := config.Get(ctx, n.Client)
	if err != nil {
		return
	}
	wx := cfg.Spec.Integrations.Messenger
	if wx == nil || wx.Webex == nil || !wx.Webex.NotifyTransitions {
		return
	}
	if err := n.Post(ctx, markdown); err != nil && !errors.Is(err, ErrNoSpace) {
		logf.FromContext(ctx).WithName("webex-notifier").Error(err, "Could not send a Webex notification")
	}
}

// ErrNoSpace means Webex is off or has no space to post to.
var ErrNoSpace = errors.New("webex is not enabled or has no space configured")

// Post sends a markdown message to the configured space, whether or not transition
// notifications are on; the cost digest uses it.
func (n *Notifier) Post(ctx context.Context, markdown string) error {
	cfg, err := config.Get(ctx, n.Client)
	if err != nil {
		return err
	}
	wx := cfg.Spec.Integrations.Messenger
	if wx == nil || wx.Webex == nil || !wx.Webex.Enabled || wx.Webex.RoomID == "" {
		return ErrNoSpace
	}
	settings, err := LoadSettings(ctx, n.Client)
	if err != nil {
		return err
	}
	if settings == nil || settings.Token == "" {
		return errors.New("webex has no bot token")
	}
	api := NewClient(settings.Token)
	if n.NewClient != nil {
		api = n.NewClient(settings.Token)
	}
	if settings.ClusterName != "" {
		markdown = "**[" + settings.ClusterName + "]** " + markdown
	}
	return api.Send(ctx, settings.RoomID, "", markdown)
}
