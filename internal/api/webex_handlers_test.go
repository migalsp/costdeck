package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // Mirrors the Webex signature scheme under test.
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	finopsv1 "github.com/migalsp/costdeck-operator/api/v1"
)

func sign(body []byte, secret string) string {
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestWebexWebhookRequiresValidSignature(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "costdeck")
	server := buildMockServer()
	ctx := context.Background()
	if err := server.Client.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "wx", Namespace: "costdeck"},
		Data:       map[string][]byte{"BOT_TOKEN": []byte("t"), "WEBHOOK_SECRET": []byte("s3cr3t")},
	}); err != nil {
		t.Fatal(err)
	}
	if err := server.Client.Create(ctx, &finopsv1.CostDeckConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "costdeck"},
		Spec: finopsv1.CostDeckConfigSpec{Integrations: finopsv1.IntegrationsConfig{
			Messenger: &finopsv1.MessengerIntegrationConfig{Webex: &finopsv1.WebexConfig{Enabled: true, SecretRef: "wx"}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	handler, err := server.Handler()
	if err != nil {
		t.Fatal(err)
	}

	// An attachment action is acknowledged without fetching anything from Webex.
	body := []byte(`{"resource":"attachmentActions","event":"created","data":{"id":"x"}}`)
	post := func(signature string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/webex/webhook", bytes.NewReader(body))
		if signature != "" {
			req.Header.Set("X-Spark-Signature", signature)
		}
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		return rr.Code
	}

	if code := post(""); code != http.StatusUnauthorized {
		t.Errorf("unsigned webhook returned %d, want 401", code)
	}
	if code := post(sign(body, "wrong")); code != http.StatusUnauthorized {
		t.Errorf("badly signed webhook returned %d, want 401", code)
	}
	if code := post(sign(body, "s3cr3t")); code != http.StatusNoContent {
		t.Errorf("correctly signed webhook returned %d, want 204", code)
	}
}
