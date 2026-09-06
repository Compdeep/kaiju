package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Compdeep/kaiju/internal/auth"
	"github.com/Compdeep/kaiju/internal/db"
)

func authAPI(t *testing.T) (*AuthAPI, *db.DB) {
	t.Helper()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("opening the test database: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	svc, err := auth.NewJWTService("test-secret-value-long-enough", dir, 24)
	if err != nil {
		t.Fatalf("token service: %v", err)
	}
	return NewAuthAPI(database, svc), database
}

func login(t *testing.T, a *AuthAPI, user, pass string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"username":"` + user + `","password":"` + pass + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(body))
	rec := httptest.NewRecorder()
	a.handleLogin(rec, req)
	return rec
}

// A node with no accounts is claimed by whoever signs in first, and what they
// type becomes the account.
//
// This replaces a setup path that ran without credentials: the configuration
// endpoint sat outside the token check because nothing else could create the
// first user, and it then stayed outside — readable and writable by anyone who
// could reach the port. Safe because of where the server listens, not because of
// anything here.
func TestTheFirstSignInClaimsAnUnclaimedNode(t *testing.T) {
	a, database := authAPI(t)

	rec := login(t, a, "owner", "a-password")
	if rec.Code != http.StatusOK {
		t.Fatalf("the first sign-in returned %d: %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if out["token"] == "" || out["token"] == nil {
		t.Error("no token was issued to the claiming account")
	}
	users, err := database.ListUsers()
	if err != nil || len(users) != 1 || users[0].Username != "owner" {
		t.Fatalf("the account was not created: %v %v", users, err)
	}
	// And it can operate the node it just claimed.
	if users[0].MaxIntent != firstUserIntent {
		t.Errorf("the claiming account has intent %d, want %d", users[0].MaxIntent, firstUserIntent)
	}
}

// Once claimed, it is an ordinary login. A second person cannot type a new
// password and be let in — that would make every node claimable forever.
func TestAClaimedNodeDoesNotAcceptNewCredentials(t *testing.T) {
	a, database := authAPI(t)
	if rec := login(t, a, "owner", "a-password"); rec.Code != http.StatusOK {
		t.Fatalf("the first sign-in failed: %s", rec.Body.String())
	}

	if rec := login(t, a, "someone-else", "their-password"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a second person got %d, want 401 — the node is claimable twice", rec.Code)
	}
	if rec := login(t, a, "owner", "the-wrong-password"); rec.Code != http.StatusUnauthorized {
		t.Errorf("the wrong password got %d, want 401", rec.Code)
	}
	if rec := login(t, a, "owner", "a-password"); rec.Code != http.StatusOK {
		t.Errorf("the owner could not sign in again: %s", rec.Body.String())
	}
	users, _ := database.ListUsers()
	if len(users) != 1 {
		t.Errorf("the node has %d accounts; claiming should have happened once", len(users))
	}
}

// Emptying the table is the recovery, and it works: the next sign-in claims it
// again. This is what `kaiju user remove` leaves behind.
func TestEmptyingTheTableMakesTheNodeClaimableAgain(t *testing.T) {
	a, database := authAPI(t)
	login(t, a, "owner", "a-password")
	if err := database.DeleteUser("owner"); err != nil {
		t.Fatalf("removing the account: %v", err)
	}

	if rec := login(t, a, "new-owner", "another-password"); rec.Code != http.StatusOK {
		t.Fatalf("the node did not become claimable again: %d %s", rec.Code, rec.Body.String())
	}
}

// A database that cannot be read is treated as CLAIMED. Answering "empty" to an
// error would let a sign-in create an account on a node that already has one.
func TestAnUnreadableDatabaseIsTreatedAsClaimed(t *testing.T) {
	a := NewAuthAPI(nil, nil)
	claimed, err := a.nodeIsClaimed()
	if err == nil {
		t.Error("no error was reported for a missing database")
	}
	if !claimed {
		t.Error("a node whose accounts cannot be counted was reported unclaimed, " +
			"which would let a sign-in claim a node that is already owned")
	}
}
