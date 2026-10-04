package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // Webex signs webhooks with HMAC-SHA1; this is not our choice.
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"

	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/migalsp/costdeck-operator/internal/config"
	"github.com/migalsp/costdeck-operator/internal/webex"
)

// webhookPayload is the envelope Webex POSTs for a "messages created" webhook.
type webhookPayload struct {
	Resource string `json:"resource"`
	Event    string `json:"event"`
	Data     struct {
		ID     string `json:"id"`
		RoomID string `json:"roomId"`
	} `json:"data"`
}

// handleWebexWebhook receives Webex webhooks. It is exempt from session authentication;
// instead every request must carry a valid X-Spark-Signature computed with the
// WEBHOOK_SECRET from the Webex credentials Secret. Without that secret, webhook delivery
// is disabled and the poller is used.
func (s *Server) handleWebexWebhook(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	log := logf.FromContext(ctx).WithName("webex-webhook")

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "could not read body")
		return
	}

	settings, err := webex.LoadSettings(ctx, s.Client)
	if err != nil {
		log.Error(err, "Could not load Webex settings")
		writeError(w, http.StatusInternalServerError, "webex is misconfigured")
		return
	}
	if settings == nil || settings.WebhookSecret == "" {
		writeError(w, http.StatusForbidden, "webhook delivery is not enabled (set WEBHOOK_SECRET in the Webex credentials Secret)")
		return
	}
	if !validWebexSignature(body, settings.WebhookSecret, r.Header.Get("X-Spark-Signature")) {
		writeError(w, http.StatusUnauthorized, "invalid webhook signature")
		return
	}

	var payload webhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		writeError(w, http.StatusBadRequest, "invalid payload")
		return
	}
	// Webex only needs a fast 2xx; anything we ignore is still acknowledged.
	if payload.Resource != "messages" || payload.Event != "created" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if settings.RoomID != "" && payload.Data.RoomID != settings.RoomID {
		log.Info("Ignoring Webex webhook from an unexpected room", "room", payload.Data.RoomID)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	go s.processWebexMessage(settings, payload.Data.ID)
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) processWebexMessage(settings *webex.Settings, messageID string) {
	ctx := s.backgroundContext()
	log := logf.FromContext(ctx).WithName("webex-webhook")
	api := webex.NewClient(settings.Token)

	me, err := api.Me(ctx)
	if err != nil {
		log.Error(err, "Could not identify the Webex bot")
		return
	}
	msg, err := api.Message(ctx, messageID)
	if err != nil {
		log.Error(err, "Could not fetch Webex message", "messageId", messageID)
		return
	}
	bot := &webex.Bot{API: api, K8s: s.Client, Namespace: config.OperatorNamespace(), ClusterName: settings.ClusterName, Me: me, Background: ctx}
	if err := bot.ProcessMessage(ctx, msg); err != nil {
		log.Error(err, "Could not process Webex message", "messageId", messageID)
	}
}

// validWebexSignature checks the HMAC-SHA1 Webex computes over the raw body.
func validWebexSignature(body []byte, secret, signature string) bool {
	if signature == "" {
		return false
	}
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}

// backgroundContext is the server's lifetime context, for work that outlives a request.
func (s *Server) backgroundContext() context.Context {
	if s.rootCtx != nil {
		return s.rootCtx
	}
	return context.Background()
}
