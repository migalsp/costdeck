package webex

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

// WebexPoller periodically polls Webex for new messages.
type WebexPoller struct {
	Client client.Client

	lastMessageID string
}

type listMessagesResponse struct {
	Items []Message `json:"items"`
}

// Start implements manager.Runnable
func (p *WebexPoller) Start(ctx context.Context) error {
	log := logf.Log.WithName("webex-poller")
	log.Info("Starting Webex Poller")

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	operatorNs := os.Getenv("POD_NAMESPACE")
	if operatorNs == "" {
		operatorNs = "costdeck"
	}

	for {
		select {
		case <-ctx.Done():
			log.Info("Stopping Webex Poller")
			return nil
		case <-ticker.C:
			// 1. Fetch config to check if Webex is enabled
			var config finopsv1.CostDeckConfig
			if err := p.Client.Get(ctx, types.NamespacedName{Name: "default", Namespace: operatorNs}, &config); err != nil {
				if !errors.IsNotFound(err) {
					log.Error(err, "Failed to get CostDeckConfig")
				}
				continue
			}

			if config.Spec.Integrations.Messenger == nil || config.Spec.Integrations.Messenger.Webex == nil || !config.Spec.Integrations.Messenger.Webex.Enabled {
				continue
			}

			secretName := config.Spec.Integrations.Messenger.Webex.SecretRef
			if secretName == "" {
				continue
			}

			var secret corev1.Secret
			if err := p.Client.Get(ctx, types.NamespacedName{Name: secretName, Namespace: operatorNs}, &secret); err != nil {
				log.Error(err, "Failed to get Webex secret")
				continue
			}

			// 2. Poll messages
			p.pollMessages(ctx, string(secret.Data["BOT_TOKEN"]), config.Spec.Integrations.Messenger.Webex.RoomID, operatorNs, config.Spec.ClusterName)
		}
	}
}

func (p *WebexPoller) pollMessages(ctx context.Context, token, roomID, operatorNs, clusterName string) {
	log := logf.FromContext(ctx).WithName("webex-poller")

	req, err := http.NewRequestWithContext(ctx, "GET", "https://webexapis.com/v1/messages?roomId="+roomID+"&mentionedPeople=me&max=5", nil)
	if err != nil {
		log.Error(err, "Failed to create request")
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Error(err, "Failed to fetch messages from Webex")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Error(fmt.Errorf("status %d", resp.StatusCode), "Failed to fetch messages")
		return
	}

	var list listMessagesResponse
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		log.Error(err, "Failed to decode Webex response")
		return
	}

	if len(list.Items) == 0 {
		return
	}

	// If this is the first time we poll, just record the latest message ID and skip processing it
	// to avoid processing old messages.
	if p.lastMessageID == "" {
		p.lastMessageID = list.Items[0].ID
		return
	}

	// We get messages in reverse chronological order (newest first).
	// We need to find all messages that are newer than p.lastMessageID.
	var newMessages []Message
	for _, msg := range list.Items {
		if msg.ID == p.lastMessageID {
			break
		}
		newMessages = append(newMessages, msg)
	}

	// Process new messages in chronological order (oldest to newest)
	bot := NewBot(token, p.Client, clusterName)
	for i := len(newMessages) - 1; i >= 0; i-- {
		msg := newMessages[i]
		if err := bot.ProcessMessage(ctx, &msg, operatorNs); err != nil {
			log.Error(err, "Failed to process message", "messageId", msg.ID)
		}
		p.lastMessageID = msg.ID
	}
}
