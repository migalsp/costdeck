package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"

	logf "sigs.k8s.io/controller-runtime/pkg/log"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/migalsp/costdeck-operator/internal/webex"
)

func (s *Server) handleWebexWebhook(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	log := logf.FromContext(ctx).WithName("api-webex")

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload webex.WebhookPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		log.Error(err, "Failed to decode Webex webhook payload")
		http.Error(w, "Invalid payload", http.StatusBadRequest)
		return
	}

	// 1. Check if Webex integration is enabled
	config := s.getOrCreateDefaultConfig(ctx)
	if config.Spec.Integrations.Messenger == nil || config.Spec.Integrations.Messenger.Webex == nil || !config.Spec.Integrations.Messenger.Webex.Enabled {
		log.Info("Webex integration is disabled, ignoring webhook")
		w.WriteHeader(http.StatusOK)
		return
	}
	wxCfg := config.Spec.Integrations.Messenger.Webex

	// Make sure this is roughly matching the room we expect. (Optional security, we can comment it if we allow cross-room management, but it's safer)
	if wxCfg.RoomID != "" && payload.Data.RoomID != "" && wxCfg.RoomID != payload.Data.RoomID {
		log.Info("Received webhook from unexpected room", "expected", wxCfg.RoomID, "got", payload.Data.RoomID)
		// We still return 200 OK because Webex expects it and doesn't need to know we ignored it.
		w.WriteHeader(http.StatusOK)
		return
	}

	operatorNs := os.Getenv("POD_NAMESPACE")
	if operatorNs == "" {
		operatorNs = "costdeck"
	}

	// 2. Fetch the bot token
	if wxCfg.SecretRef == "" {
		log.Info("No Webex credentials secret configured")
		w.WriteHeader(http.StatusOK)
		return
	}

	var secret *corev1.Secret
	var err error
	if secret, err = s.K8sClient.CoreV1().Secrets(operatorNs).Get(ctx, wxCfg.SecretRef, metav1.GetOptions{}); err != nil {
		// fallback to controller-runtime client
		var crSecret corev1.Secret
		if err2 := s.Client.Get(ctx, types.NamespacedName{Name: wxCfg.SecretRef, Namespace: operatorNs}, &crSecret); err2 != nil {
			log.Error(err2, "Failed to get Webex credentials secret")
			http.Error(w, "Failed to get credentials", http.StatusInternalServerError)
			return
		}
		secret = &crSecret
	}

	token := string(secret.Data["BOT_TOKEN"])
	if token == "" {
		log.Info("BOT_TOKEN is empty in secret")
		w.WriteHeader(http.StatusOK)
		return
	}

	// 3. Process the webhook asynchronously to avoid blocking Webex
	go func() {
		bot := webex.NewBot(token, s.Client, config.Spec.ClusterName)
		// Assuming we always respond from background context
		if err := bot.ProcessWebhook(context.Background(), payload, operatorNs); err != nil {
			log.Error(err, "Error processing webex webhook")
		}
	}()

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}
