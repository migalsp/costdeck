package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/migalsp/costdeck-operator/internal/config"
)

const (
	tokensSecretName = "costdeck-api-tokens"
	// TokenPrefix marks CostDeck API tokens so they are recognisable in logs and secret
	// scanners.
	TokenPrefix = "cdk_"
	tokenCache  = 30 * time.Second
)

var tokenName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// TokenInfo describes an API token. The token itself is never stored, only its SHA-256.
type TokenInfo struct {
	Name      string     `json:"name"`
	Role      Role       `json:"role"`
	CreatedAt time.Time  `json:"createdAt"`
	CreatedBy string     `json:"createdBy,omitempty"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	Hash      string     `json:"hash,omitempty"`
}

// Tokens manages API tokens for machine clients (MCP clients such as Claude Desktop or
// Cursor, CI pipelines, scripts). They live in the costdeck-api-tokens Secret, keyed by
// name, and are cached briefly so a bearer request does not cost an API call each time.
type Tokens struct {
	Client client.Client

	mu       sync.Mutex
	byHash   map[string]TokenInfo
	loadedAt time.Time
}

func (t *Tokens) key() client.ObjectKey {
	return client.ObjectKey{Name: tokensSecretName, Namespace: config.OperatorNamespace()}
}

func (t *Tokens) load(ctx context.Context) (*corev1.Secret, map[string]TokenInfo, error) {
	secret := &corev1.Secret{}
	err := t.Client.Get(ctx, t.key(), secret)
	if apierrors.IsNotFound(err) {
		return nil, map[string]TokenInfo{}, nil
	}
	if err != nil {
		return nil, nil, err
	}
	infos := make(map[string]TokenInfo, len(secret.Data))
	for name, raw := range secret.Data {
		var info TokenInfo
		if json.Unmarshal(raw, &info) == nil {
			info.Name = name
			infos[name] = info
		}
	}
	return secret, infos, nil
}

// List returns the tokens without their hashes.
func (t *Tokens) List(ctx context.Context) ([]TokenInfo, error) {
	_, infos, err := t.load(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]TokenInfo, 0, len(infos))
	for _, info := range infos {
		info.Hash = ""
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Create issues a token. The returned plaintext is shown once and cannot be recovered.
func (t *Tokens) Create(ctx context.Context, name string, role Role, ttl time.Duration, createdBy string) (string, TokenInfo, error) {
	if !tokenName.MatchString(name) {
		return "", TokenInfo{}, errors.New("token names are lower-case letters, digits and dashes (max 63)")
	}
	if _, ok := roleRank[role]; !ok {
		return "", TokenInfo{}, fmt.Errorf("unknown role %q", role)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", TokenInfo{}, err
	}
	token := TokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	info := TokenInfo{Name: name, Role: role, CreatedAt: time.Now().UTC().Truncate(time.Second), CreatedBy: createdBy, Hash: hashToken(token)}
	if ttl > 0 {
		exp := info.CreatedAt.Add(ttl)
		info.ExpiresAt = &exp
	}
	data, err := json.Marshal(info)
	if err != nil {
		return "", TokenInfo{}, err
	}

	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		secret, infos, err := t.load(ctx)
		if err != nil {
			return err
		}
		if _, exists := infos[name]; exists {
			return fmt.Errorf("a token named %q already exists", name)
		}
		if secret == nil {
			secret = &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name: tokensSecretName, Namespace: config.OperatorNamespace(),
					Labels: map[string]string{"app.kubernetes.io/managed-by": "costdeck-operator"},
				},
				Type: corev1.SecretTypeOpaque,
				Data: map[string][]byte{name: data},
			}
			return t.Client.Create(ctx, secret)
		}
		if secret.Data == nil {
			secret.Data = map[string][]byte{}
		}
		secret.Data[name] = data
		return t.Client.Update(ctx, secret)
	})
	if err != nil {
		return "", TokenInfo{}, err
	}
	t.invalidate()
	info.Hash = ""
	return token, info, nil
}

// Delete revokes a token. Revocation takes effect on every replica within the cache TTL.
func (t *Tokens) Delete(ctx context.Context, name string) error {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		secret, infos, err := t.load(ctx)
		if err != nil {
			return err
		}
		if _, ok := infos[name]; !ok || secret == nil {
			return apierrors.NewNotFound(corev1.Resource("token"), name)
		}
		delete(secret.Data, name)
		return t.Client.Update(ctx, secret)
	})
	t.invalidate()
	return err
}

// Authenticate resolves a bearer token to an identity.
func (t *Tokens) Authenticate(ctx context.Context, token string) (*Identity, bool) {
	if !strings.HasPrefix(token, TokenPrefix) {
		return nil, false
	}
	t.mu.Lock()
	byHash := t.byHash
	stale := time.Since(t.loadedAt) > tokenCache
	t.mu.Unlock()
	if byHash == nil || stale {
		_, infos, err := t.load(ctx)
		if err != nil {
			return nil, false
		}
		byHash = make(map[string]TokenInfo, len(infos))
		for _, info := range infos {
			byHash[info.Hash] = info
		}
		t.mu.Lock()
		t.byHash, t.loadedAt = byHash, time.Now()
		t.mu.Unlock()
	}
	info, ok := byHash[hashToken(token)]
	if !ok || (info.ExpiresAt != nil && time.Now().After(*info.ExpiresAt)) {
		return nil, false
	}
	return &Identity{Subject: "token:" + info.Name, Name: info.Name, Role: info.Role, Provider: "token"}, true
}

func (t *Tokens) invalidate() {
	t.mu.Lock()
	t.byHash = nil
	t.mu.Unlock()
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
