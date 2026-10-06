package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/migalsp/costdeck-operator/internal/config"
)

// Local users sign in with a password kept as a bcrypt hash in the costdeck-users Secret,
// one key per user. They complement the built-in admin (break-glass, managed by the Helm
// chart) and single sign-on.

const (
	usersSecretName = "costdeck-users"
	// MinPasswordLength is the shortest password a local user may choose.
	MinPasswordLength = 12
	maxPasswordLength = 72 // bcrypt ignores anything longer
	bcryptCost        = 12
	userCache         = 30 * time.Second
	// userSubjectPrefix marks the sessions of managed local users; the built-in admin's
	// subject starts with "local:".
	userSubjectPrefix = "user:"
)

var userNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]{0,61}[a-z0-9])?$`)

// Errors the API maps to 400, 403 and 404 answers.
var (
	ErrInvalidUser  = errors.New("invalid user")
	ErrUserNotFound = errors.New("user not found")
	ErrUserDisabled = errors.New("this account is disabled")
)

// User is a local account. The hash never leaves the package.
type User struct {
	Username           string    `json:"username"`
	Name               string    `json:"name,omitempty"`
	Email              string    `json:"email,omitempty"`
	Role               Role      `json:"role"`
	Disabled           bool      `json:"disabled,omitempty"`
	MustChangePassword bool      `json:"mustChangePassword,omitempty"`
	CreatedAt          time.Time `json:"createdAt"`
	CreatedBy          string    `json:"createdBy,omitempty"`
	PasswordChangedAt  time.Time `json:"passwordChangedAt"`
	Hash               string    `json:"hash,omitempty"`
}

// UserPatch changes the fields that are set.
type UserPatch struct {
	Name     *string `json:"name,omitempty"`
	Email    *string `json:"email,omitempty"`
	Role     *Role   `json:"role,omitempty"`
	Disabled *bool   `json:"disabled,omitempty"`
}

// Users manages local accounts.
type Users struct {
	Client client.Client
	// Reserved is a name no managed user may take: the built-in admin's.
	Reserved string

	mu       sync.Mutex
	byName   map[string]User
	loadedAt time.Time
}

func (u *Users) key() client.ObjectKey {
	return client.ObjectKey{Name: usersSecretName, Namespace: config.OperatorNamespace()}
}

func (u *Users) load(ctx context.Context) (*corev1.Secret, map[string]User, error) {
	secret := &corev1.Secret{}
	err := u.Client.Get(ctx, u.key(), secret)
	if apierrors.IsNotFound(err) {
		return nil, map[string]User{}, nil
	}
	if err != nil {
		return nil, nil, err
	}
	users := make(map[string]User, len(secret.Data))
	for name, raw := range secret.Data {
		var user User
		if json.Unmarshal(raw, &user) == nil {
			user.Username = name
			users[name] = user
		}
	}
	return secret, users, nil
}

// cached returns the users, read from the Secret at most every userCache. Sessions are
// checked against it on every request.
func (u *Users) cached(ctx context.Context) (map[string]User, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.byName != nil && time.Since(u.loadedAt) < userCache {
		return u.byName, nil
	}
	_, users, err := u.load(ctx)
	if err != nil {
		return nil, err
	}
	u.byName, u.loadedAt = users, time.Now()
	return users, nil
}

func (u *Users) invalidate() {
	u.mu.Lock()
	u.byName = nil
	u.mu.Unlock()
}

// Any reports whether at least one enabled local user exists.
func (u *Users) Any(ctx context.Context) bool {
	users, err := u.cached(ctx)
	if err != nil {
		return false
	}
	for _, user := range users {
		if !user.Disabled {
			return true
		}
	}
	return false
}

// List returns the users without their hashes, sorted by name.
func (u *Users) List(ctx context.Context) ([]User, error) {
	_, users, err := u.load(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]User, 0, len(users))
	for _, user := range users {
		user.Hash = ""
		out = append(out, user)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Username < out[j].Username })
	return out, nil
}

// Create adds a user. mustChange asks for a new password at the first sign-in, which is
// right whenever an administrator chose the password.
func (u *Users) Create(ctx context.Context, user User, password string, mustChange bool, by string) (User, error) {
	user.Username = strings.ToLower(strings.TrimSpace(user.Username))
	if !userNamePattern.MatchString(user.Username) {
		return User{}, fmt.Errorf("%w: user names are lower-case letters, digits, dots, dashes and underscores (max 63)", ErrInvalidUser)
	}
	if u.Reserved != "" && user.Username == strings.ToLower(u.Reserved) {
		return User{}, fmt.Errorf("%w: %q is the built-in admin account", ErrInvalidUser, user.Username)
	}
	if err := validateProfile(&user); err != nil {
		return User{}, err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return User{}, err
	}
	now := time.Now().UTC().Truncate(time.Second)
	user.Hash, user.CreatedAt, user.CreatedBy, user.PasswordChangedAt = hash, now, by, now
	user.MustChangePassword, user.Disabled = mustChange, false
	err = u.write(ctx, user.Username, func(existing map[string]User) (*User, error) {
		if _, ok := existing[user.Username]; ok {
			return nil, fmt.Errorf("%w: a user named %q already exists", ErrInvalidUser, user.Username)
		}
		return &user, nil
	})
	user.Hash = ""
	return user, err
}

// Update changes a user's profile, role or disabled flag.
func (u *Users) Update(ctx context.Context, username string, patch UserPatch) (User, error) {
	var out User
	err := u.write(ctx, username, func(existing map[string]User) (*User, error) {
		user, ok := existing[username]
		if !ok {
			return nil, ErrUserNotFound
		}
		if patch.Name != nil {
			user.Name = *patch.Name
		}
		if patch.Email != nil {
			user.Email = *patch.Email
		}
		if patch.Role != nil {
			user.Role = *patch.Role
		}
		if patch.Disabled != nil {
			user.Disabled = *patch.Disabled
		}
		if err := validateProfile(&user); err != nil {
			return nil, err
		}
		out = user
		return &user, nil
	})
	out.Hash = ""
	return out, err
}

// SetPassword replaces a user's password and ends their other sessions.
func (u *Users) SetPassword(ctx context.Context, username, password string, mustChange bool) error {
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	return u.write(ctx, username, func(existing map[string]User) (*User, error) {
		user, ok := existing[username]
		if !ok {
			return nil, ErrUserNotFound
		}
		// Sessions issued before this second end; the one re-issued to a user who just
		// changed their own password is issued within it and stays valid.
		user.Hash, user.MustChangePassword = hash, mustChange
		user.PasswordChangedAt = time.Now().UTC().Truncate(time.Second)
		return &user, nil
	})
}

// Delete removes a user; their sessions end within the cache TTL on every replica.
func (u *Users) Delete(ctx context.Context, username string) error {
	return u.write(ctx, username, func(existing map[string]User) (*User, error) {
		if _, ok := existing[username]; !ok {
			return nil, ErrUserNotFound
		}
		return nil, nil
	})
}

// write applies change to one user under optimistic concurrency. change returns the new
// record, or nil to delete it.
func (u *Users) write(ctx context.Context, username string, change func(map[string]User) (*User, error)) error {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		secret, users, err := u.load(ctx)
		if err != nil {
			return err
		}
		next, err := change(users)
		if err != nil {
			return err
		}
		if next == nil {
			delete(secret.Data, username)
			return u.Client.Update(ctx, secret)
		}
		record := *next
		record.Username = ""
		data, err := json.Marshal(record)
		if err != nil {
			return err
		}
		if secret == nil {
			secret = &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name: usersSecretName, Namespace: config.OperatorNamespace(),
					Labels: map[string]string{"app.kubernetes.io/managed-by": "costdeck-operator"},
				},
				Type: corev1.SecretTypeOpaque,
				Data: map[string][]byte{username: data},
			}
			return u.Client.Create(ctx, secret)
		}
		if secret.Data == nil {
			secret.Data = map[string][]byte{}
		}
		secret.Data[username] = data
		return u.Client.Update(ctx, secret)
	})
	u.invalidate()
	return err
}

// dummyHash is compared against when a user does not exist, so that a wrong name takes
// as long as a wrong password and names cannot be probed. It is made on first use.
var dummyHash = sync.OnceValue(func() []byte {
	h, _ := bcrypt.GenerateFromPassword([]byte("costdeck-no-such-user"), bcryptCost)
	return h
})

// Authenticate checks a password against the Secret itself, not the cache, so a password
// changed on another replica applies at once.
func (u *Users) Authenticate(ctx context.Context, username, password string) (User, error) {
	_, users, err := u.load(ctx)
	if err != nil {
		return User{}, err
	}
	user, ok := users[strings.ToLower(strings.TrimSpace(username))]
	hash := []byte(user.Hash)
	if !ok {
		hash = dummyHash()
	}
	if bcrypt.CompareHashAndPassword(hash, []byte(password)) != nil || !ok {
		return User{}, ErrUserNotFound
	}
	if user.Disabled {
		return User{}, ErrUserDisabled
	}
	user.Hash = ""
	return user, nil
}

// Session checks a managed user's session against the current account: it ends when the
// user is deleted or disabled or their password changed after it was issued, and it
// carries the user's current role.
func (u *Users) Session(ctx context.Context, id *Identity) (User, error) {
	users, err := u.cached(ctx)
	if err != nil {
		return User{}, err
	}
	user, ok := users[strings.TrimPrefix(id.Subject, userSubjectPrefix)]
	switch {
	case !ok:
		return User{}, ErrUserNotFound
	case user.Disabled:
		return User{}, ErrUserDisabled
	case id.IssuedAt > 0 && id.IssuedAt < user.PasswordChangedAt.Unix():
		return User{}, errors.New("the password was changed; sign in again")
	}
	user.Hash = ""
	return user, nil
}

// Identity is the session identity of a user.
func (user User) Identity() Identity {
	return Identity{
		Subject: userSubjectPrefix + user.Username, Name: firstNonEmpty(user.Name, user.Username), Email: user.Email,
		Role: user.Role, Provider: "local", MustChangePassword: user.MustChangePassword,
	}
}

// ManagedUser returns the name of the managed local user behind a session, if it is one.
func ManagedUser(id *Identity) (string, bool) {
	if id == nil {
		return "", false
	}
	return strings.CutPrefix(id.Subject, userSubjectPrefix)
}

func validateProfile(user *User) error {
	user.Name, user.Email = strings.TrimSpace(user.Name), strings.TrimSpace(user.Email)
	if _, ok := roleRank[user.Role]; !ok {
		return fmt.Errorf("%w: role must be viewer, operator or admin", ErrInvalidUser)
	}
	if utf8.RuneCountInString(user.Name) > 100 || len(user.Email) > 254 {
		return fmt.Errorf("%w: the name or email is too long", ErrInvalidUser)
	}
	if user.Email != "" && (!strings.Contains(user.Email, "@") || strings.ContainsAny(user.Email, " \t\r\n")) {
		return fmt.Errorf("%w: %q is not an email address", ErrInvalidUser, user.Email)
	}
	return nil
}

func hashPassword(password string) (string, error) {
	switch n := len(password); {
	case utf8.RuneCountInString(password) < MinPasswordLength:
		return "", fmt.Errorf("%w: passwords have at least %d characters", ErrInvalidUser, MinPasswordLength)
	case n > maxPasswordLength:
		return "", fmt.Errorf("%w: passwords have at most %d bytes", ErrInvalidUser, maxPasswordLength)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// GeneratePassword returns a random password for an administrator to hand over.
func GeneratePassword() string {
	return rand.Text()
}

// SignIn records who signed in, how and when, for the users page.
type SignIn struct {
	Subject  string    `json:"subject"`
	Name     string    `json:"name"`
	Email    string    `json:"email,omitempty"`
	Provider string    `json:"provider"`
	Role     Role      `json:"role"`
	At       time.Time `json:"at"`
	Count    int       `json:"count"`
}

const (
	signInsSecretName = "costdeck-sign-ins"
	maxSignIns        = 500
)

// SignIns keeps the last sign-in of every identity, in a Secret because it holds names
// and email addresses.
type SignIns struct {
	Client client.Client
}

func (s *SignIns) key() client.ObjectKey {
	return client.ObjectKey{Name: signInsSecretName, Namespace: config.OperatorNamespace()}
}

// signInKey turns a subject into a Secret data key; subjects contain colons.
func signInKey(subject string) string {
	sum := sha256.Sum256([]byte(subject))
	return hex.EncodeToString(sum[:12])
}

// Record notes a sign-in. Failing to record never fails the sign-in.
func (s *SignIns) Record(ctx context.Context, id Identity) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		secret := &corev1.Secret{}
		err := s.Client.Get(ctx, s.key(), secret)
		create := apierrors.IsNotFound(err)
		if err != nil && !create {
			return err
		}
		if secret.Data == nil {
			secret.Data = map[string][]byte{}
		}
		k := signInKey(id.Subject)
		var prev SignIn
		_ = json.Unmarshal(secret.Data[k], &prev)
		rec := SignIn{Subject: id.Subject, Name: id.Name, Email: id.Email, Provider: id.Provider, Role: id.Role,
			At: time.Now().UTC().Truncate(time.Second), Count: prev.Count + 1}
		data, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		secret.Data[k] = data
		pruneSignIns(secret.Data)
		if create {
			secret.ObjectMeta = metav1.ObjectMeta{
				Name: signInsSecretName, Namespace: config.OperatorNamespace(),
				Labels: map[string]string{"app.kubernetes.io/managed-by": "costdeck-operator"},
			}
			secret.Type = corev1.SecretTypeOpaque
			return s.Client.Create(ctx, secret)
		}
		return s.Client.Update(ctx, secret)
	})
}

// pruneSignIns keeps the most recent maxSignIns records.
func pruneSignIns(data map[string][]byte) {
	if len(data) <= maxSignIns {
		return
	}
	type entry struct {
		key string
		at  time.Time
	}
	entries := make([]entry, 0, len(data))
	for k, raw := range data {
		var rec SignIn
		_ = json.Unmarshal(raw, &rec)
		entries = append(entries, entry{k, rec.At})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].at.After(entries[j].at) })
	for _, e := range entries[maxSignIns:] {
		delete(data, e.key)
	}
}

// List returns the recorded sign-ins, most recent first.
func (s *SignIns) List(ctx context.Context) ([]SignIn, error) {
	secret := &corev1.Secret{}
	if err := s.Client.Get(ctx, s.key(), secret); err != nil {
		if apierrors.IsNotFound(err) {
			return []SignIn{}, nil
		}
		return nil, err
	}
	out := make([]SignIn, 0, len(secret.Data))
	for _, raw := range secret.Data {
		var rec SignIn
		if json.Unmarshal(raw, &rec) == nil {
			out = append(out, rec)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out, nil
}
