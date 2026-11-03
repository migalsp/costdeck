package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestTokensStoreOnlyTheHash(t *testing.T) {
	svc := newTestService(t)
	tokens := &Tokens{Client: svc.Client}
	ctx := context.Background()
	token, _, err := tokens.Create(ctx, "ci", RoleOperator, 0, "local:admin")
	if err != nil {
		t.Fatal(err)
	}

	var secret corev1.Secret
	if err := svc.Client.Get(ctx, client.ObjectKey{Namespace: "costdeck", Name: tokensSecretName}, &secret); err != nil {
		t.Fatal(err)
	}
	raw := string(secret.Data["ci"])
	secretPart := strings.TrimPrefix(token, TokenPrefix)
	if strings.Contains(raw, secretPart) {
		t.Fatalf("the Secret holds the plaintext token: %s", raw)
	}
	var stored TokenInfo
	if err := json.Unmarshal(secret.Data["ci"], &stored); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(token))
	if stored.Hash != hex.EncodeToString(sum[:]) {
		t.Errorf("stored hash = %q, want the SHA-256 of the token", stored.Hash)
	}
	if stored.Role != RoleOperator || stored.CreatedBy != "local:admin" || stored.ExpiresAt != nil {
		t.Errorf("stored token = %+v", stored)
	}

	if _, ok := tokens.Authenticate(ctx, stored.Hash); ok {
		t.Error("the stored hash itself was accepted as a token")
	}
	if _, ok := tokens.Authenticate(ctx, TokenPrefix+stored.Hash); ok {
		t.Error("someone who can read the Secret must not be able to use the hash as a token")
	}
	if _, ok := tokens.Authenticate(ctx, secretPart); ok {
		t.Error("a token without the cdk_ prefix was accepted")
	}
}

func TestTokensCreateRefusesUnknownRoles(t *testing.T) {
	tokens := &Tokens{Client: newTestService(t).Client}
	if _, _, err := tokens.Create(context.Background(), "ci", Role("root"), 0, ""); err == nil || !strings.Contains(err.Error(), `unknown role "root"`) {
		t.Errorf("Create with an unknown role: %v", err)
	}
	list, err := tokens.List(context.Background())
	if err != nil || len(list) != 0 {
		t.Errorf("a refused token was stored: %+v, %v", list, err)
	}
}

func TestTokenRevocationReachesOtherReplicasWithinTheCache(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	replicaA := &Tokens{Client: svc.Client}
	replicaB := &Tokens{Client: svc.Client}
	token, _, err := replicaA.Create(ctx, "laptop", RoleAdmin, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := replicaB.Authenticate(ctx, token); !ok {
		t.Fatal("replica B rejects a valid token")
	}

	if err := replicaA.Delete(ctx, "laptop"); err != nil {
		t.Fatal(err)
	}
	if _, ok := replicaA.Authenticate(ctx, token); ok {
		t.Error("the replica that revoked a token still accepts it")
	}

	replicaB.mu.Lock()
	replicaB.loadedAt = time.Now().Add(-tokenCache - time.Second)
	replicaB.mu.Unlock()
	if _, ok := replicaB.Authenticate(ctx, token); ok {
		t.Error("another replica still accepts a revoked token after its cache expired")
	}

	if err := replicaA.Delete(ctx, "laptop"); !apierrors.IsNotFound(err) {
		t.Errorf("revoking an unknown token = %v, want not found", err)
	}
}
