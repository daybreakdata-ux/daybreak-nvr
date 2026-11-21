package engine

import (
	"strings"
	"testing"
)

// Tests run with an isolated config dir so they never touch a
// real users.json.
func testUsersEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

func TestUsersFallbackAdminListed(t *testing.T) {
	testUsersEnv(t)
	// The baked fallback hash must be exactly 60 characters (a
	// 59-character copy — missing the final "." — 401s every
	// login, because bcrypt hashes are exactly 60 chars).
	if len(FallbackAdminHash) != 60 {
		t.Fatalf("FallbackAdminHash length = %d, want 60", len(FallbackAdminHash))
	}
	users := EffectiveUsers()
	if len(users) != 1 || users[0].Username != "admin" || users[0].Role != RoleAdmin || !users[0].BuiltIn {
		t.Fatalf("fresh store should list only the built-in admin, got %+v", users)
	}
	if users[0].PasswordHash != FallbackAdminHash {
		t.Fatal("built-in admin hash mismatch")
	}
}

func TestUsersSetAndAuthenticate(t *testing.T) {
	testUsersEnv(t)
	u, err := SetUser("viewer1", "secret-pass", RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	if u.Role != RoleViewer {
		t.Fatalf("role = %q", u.Role)
	}
	got, ok := Authenticate("viewer1", "secret-pass")
	if !ok || got.Username != "viewer1" || got.Role != RoleViewer {
		t.Fatalf("authenticate stored user = %+v, %v", got, ok)
	}
	if _, ok := Authenticate("viewer1", "wrong"); ok {
		t.Fatal("wrong password authenticated")
	}
	if _, ok := Authenticate("nobody", "secret-pass"); ok {
		t.Fatal("unknown user authenticated")
	}
	// Role change without a password keeps the hash.
	if _, err := SetUser("viewer1", "", RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if got, ok := Authenticate("viewer1", "secret-pass"); !ok || got.Role != RoleAdmin {
		t.Fatalf("role change broke auth: %+v, %v", got, ok)
	}
	// Bad inputs rejected.
	if _, err := SetUser("viewer1", "x", RoleViewer); err == nil {
		t.Fatal("short password accepted")
	}
	if _, err := SetUser("viewer1", "long-enough", "superuser"); err == nil {
		t.Fatal("bad role accepted")
	}
	if _, err := SetUser("", "long-enough", RoleViewer); err == nil {
		t.Fatal("empty username accepted")
	}
	if _, err := SetUser("newuser", "", RoleViewer); err == nil {
		t.Fatal("new user without password accepted")
	}
}

func TestUsersAdminOverrideAndDelete(t *testing.T) {
	testUsersEnv(t)
	// Changing the admin password writes a stored entry that
	// overrides the fallback (the stored entry is not built-in).
	u, err := SetUser("admin", "new-admin-pass", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if u.BuiltIn {
		t.Fatal("stored admin must not be marked built-in")
	}
	users := EffectiveUsers()
	if len(users) != 1 || users[0].BuiltIn {
		t.Fatalf("stored admin should replace the built-in entry, got %+v", users)
	}
	if got, ok := Authenticate("admin", "new-admin-pass"); !ok || got.Role != RoleAdmin {
		t.Fatalf("stored admin did not authenticate: %+v, %v", got, ok)
	}
	// The built-in admin cannot be deleted.
	if err := DeleteUser("admin"); err == nil {
		t.Fatal("deleting admin was allowed")
	}
	// A normal user can be deleted and then no longer signs in.
	if _, err := SetUser("temp", "temp-pass", RoleViewer); err != nil {
		t.Fatal(err)
	}
	if err := DeleteUser("temp"); err != nil {
		t.Fatal(err)
	}
	if _, ok := Authenticate("temp", "temp-pass"); ok {
		t.Fatal("deleted user still authenticates")
	}
	if err := DeleteUser("ghost"); err == nil {
		t.Fatal("deleting unknown user did not error")
	}
}

func TestUsersSigChanges(t *testing.T) {
	testUsersEnv(t)
	before := UsersSig(EffectiveUsers())
	if _, err := SetUser("a", "pass-a1", RoleViewer); err != nil {
		t.Fatal(err)
	}
	if UsersSig(EffectiveUsers()) == before {
		t.Fatal("user add did not change the sync signature")
	}
	if !strings.Contains(UsersSig(EffectiveUsers()), "a=viewer=") {
		t.Fatal("signature missing the new user")
	}
}
