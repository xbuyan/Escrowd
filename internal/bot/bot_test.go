package bot

import (
	"os"
	"testing"
)

// This is a regression test for a real bug: the original code called
// os.Getenv() with a literal Discord user ID as the *variable name*
// (os.Getenv("1398616689976807498")) instead of comparing against it as a
// value. Since no env var is ever named that, it always returned "", so
// the check `m.Author.ID != ""` was true for every real user — meaning
// !escrow resolve and !escrow backup silently rejected everyone,
// including the actual admin.

func withAdminDiscordID(t *testing.T, id string) {
	t.Helper()
	old := os.Getenv("ESCROWD_ADMIN_DISCORD_ID")
	os.Setenv("ESCROWD_ADMIN_DISCORD_ID", id)
	t.Cleanup(func() { os.Setenv("ESCROWD_ADMIN_DISCORD_ID", old) })
}

func TestIsAdmin_MatchesConfiguredID(t *testing.T) {
	withAdminDiscordID(t, "1398616689976807498")

	if !isAdmin("1398616689976807498") {
		t.Fatal("expected the configured admin ID to be recognized as admin")
	}
}

func TestIsAdmin_RejectsOtherUsers(t *testing.T) {
	withAdminDiscordID(t, "1398616689976807498")

	if isAdmin("some-other-user-id") {
		t.Fatal("expected a non-admin user ID to be rejected")
	}
}

// This is the specific case that was broken: even the real admin's own ID
// must be accepted. Bug of this shape (comparing against os.Getenv of a
// literal ID) would fail every user including this one.
func TestIsAdmin_RejectsEveryoneWhenEnvVarUnset(t *testing.T) {
	withAdminDiscordID(t, "")

	if isAdmin("1398616689976807498") {
		t.Fatal("expected isAdmin to reject everyone (fail closed) when ESCROWD_ADMIN_DISCORD_ID is unset")
	}
	if isAdmin("") {
		t.Fatal("expected isAdmin to reject an empty user ID even when the env var is also empty — must not accept on a blank-vs-blank match")
	}
}
