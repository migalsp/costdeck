package webex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

// WebhookPayload represents the incoming JSON from Webex.
type WebhookPayload struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Resource string `json:"resource"`
	Event    string `json:"event"`
	Data     struct {
		ID          string `json:"id"`
		RoomID      string `json:"roomId"`
		PersonID    string `json:"personId"`
		PersonEmail string `json:"personEmail"`
		Created     string `json:"created"`
	} `json:"data"`
}

// Message represents a Webex message.
type Message struct {
	ID          string `json:"id"`
	RoomID      string `json:"roomId"`
	Text        string `json:"text"`
	PersonEmail string `json:"personEmail"`
}

// Bot handles interactions with the Webex API and processing commands.
type Bot struct {
	Token       string
	K8sClient   client.Client
	ClusterName string
}

// NewBot creates a new Webex bot instance.
func NewBot(token string, k8sClient client.Client, clusterName string) *Bot {
	return &Bot{
		Token:       token,
		K8sClient:   k8sClient,
		ClusterName: clusterName,
	}
}

// ProcessWebhook parses the webhook payload, fetches the message text, and executes the command.
func (b *Bot) ProcessWebhook(ctx context.Context, payload WebhookPayload, operatorNamespace string) error {
	log := logf.FromContext(ctx).WithName("webex-bot")

	// We only care about new messages.
	if payload.Resource != "messages" || payload.Event != "created" {
		log.V(1).Info("Ignoring non-message/created webhook", "resource", payload.Resource, "event", payload.Event)
		return nil
	}

	// Fetch message details
	msg, err := b.getMessage(ctx, payload.Data.ID)
	if err != nil {
		return fmt.Errorf("failed to get message: %w", err)
	}

	return b.ProcessMessage(ctx, msg, operatorNamespace)
}

// ProcessMessage processes a Webex message.
func (b *Bot) ProcessMessage(ctx context.Context, msg *Message, operatorNamespace string) error {
	log := logf.FromContext(ctx).WithName("webex-bot")

	// Ignore messages from the bot itself (typically ends with @webex.bot)
	if strings.HasSuffix(msg.PersonEmail, "webex.bot") {
		return nil
	}

	text := strings.TrimSpace(msg.Text)
	log.Info("Received Webex message", "room", msg.RoomID, "text", text)

	// Basic matching logic: e.g., "/scale group frontend up"
	parts := strings.Fields(text)

	// Strip out bot's name if it appears first (Webex mentions inject the bot's display name)
	if len(parts) > 0 && strings.ToLower(parts[0]) == "costdeck" {
		parts = parts[1:]
	}

	isGlobalHelp := len(parts) > 0 && strings.ToLower(parts[0]) == "help"

	if b.ClusterName != "" && !isGlobalHelp {
		if len(parts) == 0 || !strings.EqualFold(parts[0], b.ClusterName) {
			// This message is not intended for this cluster. Silently ignore.
			return nil
		}
		// Strip the cluster name
		parts = parts[1:]
	}

	cleanText := strings.Join(parts, " ")

	if len(parts) >= 3 && (parts[0] == "/status" || parts[0] == "status") {
		targetType := parts[1]
		targetName := parts[2]

		switch targetType {
		case "group":
			var group finopsv1.ScalingGroup
			if err := b.K8sClient.Get(ctx, types.NamespacedName{Name: targetName, Namespace: operatorNamespace}, &group); err == nil {
				var outMsg strings.Builder
				outMsg.WriteString(fmt.Sprintf("**Group `%s`**\nStatus: `%s`\nNamespaces: %d/%d ready\nManaged Namespaces:\n", targetName, group.Status.Phase, group.Status.NamespacesReady, group.Status.NamespacesTotal))
				for _, ns := range group.Spec.Namespaces {
					outMsg.WriteString(fmt.Sprintf("- `%s`\n", ns))
				}
				b.SendMessage(ctx, msg.RoomID, outMsg.String())
				return nil
			} else {
				b.SendMessage(ctx, msg.RoomID, fmt.Sprintf("Group `%s` not found.", targetName))
				return nil
			}
		case "config":
			var conf finopsv1.ScalingConfig
			if err := b.K8sClient.Get(ctx, types.NamespacedName{Name: targetName, Namespace: operatorNamespace}, &conf); err == nil {
				b.SendMessage(ctx, msg.RoomID, fmt.Sprintf("**Config `%s`**\nStatus: `%s`", targetName, conf.Status.Phase))
				return nil
			} else {
				b.SendMessage(ctx, msg.RoomID, fmt.Sprintf("Config `%s` not found.", targetName))
				return nil
			}
		}
	}

	if len(parts) >= 2 && (parts[0] == "/status" || parts[0] == "status") && parts[1] == "namespaces" {
		var nsList finopsv1.ScalingConfigList
		if err := b.K8sClient.List(ctx, &nsList); err == nil {
			var activeNS, inactiveNS []string
			for _, ns := range nsList.Items {
				if ns.Spec.Active != nil && !*ns.Spec.Active {
					inactiveNS = append(inactiveNS, ns.Name)
				} else {
					activeNS = append(activeNS, ns.Name)
				}
			}
			var outMsg strings.Builder
			outMsg.WriteString("**Namespaces Status**\n\n**Active:**\n")
			for _, ns := range activeNS {
				outMsg.WriteString(fmt.Sprintf("- `%s`\n", ns))
			}
			outMsg.WriteString("\n**Inactive (Scaled Down):**\n")
			for _, ns := range inactiveNS {
				outMsg.WriteString(fmt.Sprintf("- `%s`\n", ns))
			}
			b.SendMessage(ctx, msg.RoomID, outMsg.String())
			return nil
		}
	}

	if len(parts) >= 4 && (parts[0] == "/scale" || parts[0] == "scale") {
		targetType := parts[1] // "group" or "config"
		targetName := parts[2]
		action := parts[3] // "up" or "down"

		var active bool
		switch action {
		case "up":
			active = true
		case "down":
			active = false
		default:
			return b.SendMessage(ctx, msg.RoomID, "Invalid action. Use 'up' or 'down'.")
		}

		err := b.executeScale(ctx, targetType, targetName, active, operatorNamespace)
		if err != nil {
			log.Error(err, "Failed to execute scale command from Webex")
			b.SendMessage(context.Background(), msg.RoomID, fmt.Sprintf("Failed to scale %s %s: %v", targetType, targetName, err))
			return err
		}

		statusWords := "scale down"
		targetPhase := "ScaledDown"
		if active {
			statusWords = "scale up"
			targetPhase = "ScaledUp"
		}

		b.SendMessage(context.Background(), msg.RoomID, fmt.Sprintf("🚀 Initiated %s for **%s** `%s`...", statusWords, targetType, targetName))

		// Background goroutine to monitor progress
		go func(targetType, targetName, targetPhase string, active bool) {
			monitorCtx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
			defer cancel()

			ticker := time.NewTicker(1 * time.Minute)
			defer ticker.Stop()

			for {
				select {
				case <-monitorCtx.Done():
					b.SendMessage(context.Background(), msg.RoomID, fmt.Sprintf("⚠️ Timed out waiting for **%s** `%s` to finish scaling.", targetType, targetName))
					return
				case <-ticker.C:
					var currentPhase string
					var progress string
					var overridden bool

					switch targetType {
					case "group":
						var group finopsv1.ScalingGroup
						if err := b.K8sClient.Get(context.Background(), types.NamespacedName{Name: targetName, Namespace: operatorNamespace}, &group); err == nil {
							if group.Spec.Active != nil && *group.Spec.Active != active {
								overridden = true
							}
							currentPhase = group.Status.Phase
							if group.Status.NamespacesTotal > 0 {
								progress = fmt.Sprintf(" - %d/%d namespaces ready", group.Status.NamespacesReady, group.Status.NamespacesTotal)
							}
						}
					case "config":
						var conf finopsv1.ScalingConfig
						if err := b.K8sClient.Get(context.Background(), types.NamespacedName{Name: targetName, Namespace: operatorNamespace}, &conf); err == nil {
							if conf.Spec.Active != nil && *conf.Spec.Active != active {
								overridden = true
							}
							currentPhase = conf.Status.Phase
						}
					}

					if overridden {
						actionStr := "UP"
						if !active {
							actionStr = "DOWN"
						}
						// Silently abort, or notify it was interrupted. We'll just return silently to avoid spam.
						log.Info(fmt.Sprintf("Scaling %s for %s %s was overridden by a newer command. Aborting monitor.", actionStr, targetType, targetName))
						return
					}

					if currentPhase == targetPhase {
						statusWordsDone := "scaled DOWN"
						if active {
							statusWordsDone = "scaled UP"
						}
						b.SendMessage(context.Background(), msg.RoomID, fmt.Sprintf("✅ **%s** `%s` has been successfully %s via CostDeck.", capitalize(targetType), targetName, statusWordsDone))
						return
					} else if currentPhase != "" {
						b.SendMessage(context.Background(), msg.RoomID, fmt.Sprintf("⏳ Still scaling **%s** `%s` (Current Status: %s%s)...", targetType, targetName, currentPhase, progress))
					}
				}
			}
		}(targetType, targetName, targetPhase, active)

		return nil
	}

	// For help or unknown commands, we can send a simple helper message if it mentions scaling.
	if strings.Contains(strings.ToLower(cleanText), "help") || strings.HasPrefix(strings.ToLower(cleanText), "/help") || strings.HasPrefix(cleanText, "/scale") || strings.HasPrefix(cleanText, "scale") || strings.HasPrefix(cleanText, "/status") || strings.HasPrefix(cleanText, "status") || len(parts) == 0 {
		prefix := ""
		clusterDisplay := "Default/Global"
		if b.ClusterName != "" {
			prefix = b.ClusterName + " "
			clusterDisplay = b.ClusterName
		}
		helpMsg := fmt.Sprintf("Hi there! I am the CostDeck Scaling Bot for cluster: **%s**.\n\n", clusterDisplay) +
			"Available Commands:\n" +
			fmt.Sprintf("- `%sscale group <group-name> up` : Scale up a scaling group.\n", prefix) +
			fmt.Sprintf("- `%sscale group <group-name> down` : Scale down a scaling group.\n", prefix) +
			fmt.Sprintf("- `%sscale config <namespace> up` : Scale up an individual namespace config.\n", prefix) +
			fmt.Sprintf("- `%sscale config <namespace> down` : Scale down an individual namespace config.\n", prefix) +
			fmt.Sprintf("- `%sstatus group <group-name>` : Show status of a scaling group.\n", prefix) +
			fmt.Sprintf("- `%sstatus config <namespace>` : Show status of a namespace config.\n", prefix) +
			fmt.Sprintf("- `%sstatus namespaces` : Show up/down status of all managed namespaces.\n", prefix) +
			"- `help` : Show this help message (all clusters will respond).\n\n" +
			"*(Tip: If there are multiple CostDeck clusters in this room, prefix your command with the cluster name to target a specific one, as shown in the examples above!)*"
		b.SendMessage(context.Background(), msg.RoomID, helpMsg)
	}

	return nil
}

// executeScale updates the active state on ScalingGroup or ScalingConfig.
func (b *Bot) executeScale(ctx context.Context, targetType, targetName string, active bool, namespace string) error {
	switch targetType {
	case "group":
		var group finopsv1.ScalingGroup
		err := b.K8sClient.Get(ctx, types.NamespacedName{Name: targetName, Namespace: namespace}, &group)
		if err != nil {
			if errors.IsNotFound(err) {
				return fmt.Errorf("group '%s' not found", targetName)
			}
			return err
		}

		// Prevent overriding an in-progress scaling operation
		if group.Status.Phase == "ScalingUp" || group.Status.Phase == "ScalingDown" {
			return fmt.Errorf("Please wait for the current scaling operation to finish (Current Status: %s)", group.Status.Phase)
		}

		// Prevent redundant commands
		if active && group.Status.Phase == "ScaledUp" {
			return fmt.Errorf("already scaled UP")
		}
		if !active && group.Status.Phase == "ScaledDown" {
			return fmt.Errorf("already scaled DOWN")
		}

		var finalActive = active
		group.Spec.Active = &finalActive
		return b.K8sClient.Update(ctx, &group)

	case "config":
		var conf finopsv1.ScalingConfig
		err := b.K8sClient.Get(ctx, types.NamespacedName{Name: targetName, Namespace: namespace}, &conf)
		if err != nil {
			if errors.IsNotFound(err) {
				// Trying to fallback to costdeck-system if we're in another ns, or vice-versa
				return fmt.Errorf("namespace config '%s' not found", targetName)
			}
			return err
		}

		// Prevent overriding an in-progress scaling operation
		if conf.Status.Phase == "ScalingUp" || conf.Status.Phase == "ScalingDown" {
			return fmt.Errorf("Please wait for the current scaling operation to finish (Current Status: %s)", conf.Status.Phase)
		}

		// Prevent redundant commands
		if active && conf.Status.Phase == "ScaledUp" {
			return fmt.Errorf("already scaled UP")
		}
		if !active && conf.Status.Phase == "ScaledDown" {
			return fmt.Errorf("already scaled DOWN")
		}

		var finalActive = active
		conf.Spec.Active = &finalActive
		return b.K8sClient.Update(ctx, &conf)

	default:
		return fmt.Errorf("unknown target type: %s. Use 'group' or 'config'", targetType)
	}
}

func (b *Bot) getMessage(ctx context.Context, messageID string) (*Message, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://webexapis.com/v1/messages/"+messageID, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+b.Token)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("webex API returned status %d: %s", resp.StatusCode, string(body))
	}

	var msg Message
	if err := json.NewDecoder(resp.Body).Decode(&msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

// SendMessage sends a markdown message to the specified Webex Room.
func (b *Bot) SendMessage(ctx context.Context, roomID, markdown string) error {
	payload := map[string]any{
		"roomId":   roomID,
		"markdown": markdown,
	}
	bodyData, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "https://webexapis.com/v1/messages", bytes.NewReader(bodyData))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+b.Token)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		out, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("webex API returned status %d: %s", resp.StatusCode, string(out))
	}

	return nil
}

// capitalize returns the string with the first letter upper-cased.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
