package ui

import (
	"github.com/Compdeep/kaiju/internal/auth"
	"github.com/Compdeep/kaiju/internal/db"
)

// The two things the interface needs from outside itself, and neither is a
// struct an application fills in.
//
// Both are interfaces with no exported methods, satisfied only by what the
// constructors here return. That is deliberate and it is not an interface
// pretending to be a plugin point. It exists so that what actually crosses the
// boundary is a name and not a schema: kaiju keeps its storage and its tokens
// under internal/, is free to change either, and an application that holds one
// of these keeps compiling. On the day an application needs to supply its own,
// the methods go here and this signature does not move.
//
// Both are optional, and absent means the feature is off rather than broken.

/*
 * Store is where the interface keeps conversations and the accounts behind them.
 * desc: Nil is legal and means the interface keeps nothing: a conversation
 *       works, every request stands alone, there is no history to load and no
 *       account to sign in to. The routes that would read it are not registered
 *       at all, so nothing answers with an empty list where a list was expected.
 */
type Store interface {
	// database is unexported, so no type outside kaiju can satisfy Store.
	database() *db.DB
}

/*
 * Authenticator decides who is asking.
 * desc: Nil is legal and means there is no sign-in. Every request is then one
 *       unnamed local operator with no tool restriction, which is the right
 *       shape only where the transport is the boundary — an interface bound to
 *       a loopback address, reached by whoever is already on the machine. It is
 *       the wrong shape on any address someone else can reach.
 *
 *       An Authenticator without a Store cannot authenticate anybody, because
 *       the accounts live in the store. Handler refuses that pair rather than
 *       serving a sign-in page that can never succeed.
 */
type Authenticator interface {
	// jwt is unexported, so no type outside kaiju can satisfy Authenticator.
	jwt() *auth.JWTService
}

// ── kaiju's own implementations ──────────────────────────────────────────────

type kaijuStore struct{ db *db.DB }

func (s kaijuStore) database() *db.DB { return s.db }

/*
 * StoreOf wraps kaiju's own database as a Store.
 * desc: Reachable only from inside kaiju: db.DB lives under internal/, so no
 *       other module can name the argument, let alone hold one. It takes an
 *       open database rather than a path because kaiju's daemon opens one for
 *       its own reasons — the user command, the intent registry, the clearance
 *       endpoints — and the interface must be given that same handle rather
 *       than a second one onto the same file.
 * param: database - an open database, or nil.
 * return: a Store, or nil if the database was nil, so a caller can pass the
 *         result straight through without testing it first.
 */
func StoreOf(database *db.DB) Store {
	if database == nil {
		return nil
	}
	return kaijuStore{db: database}
}

type kaijuAuth struct{ svc *auth.JWTService }

func (a kaijuAuth) jwt() *auth.JWTService { return a.svc }

/*
 * NewAuthenticator builds kaiju's own token service.
 * desc: Signs and validates the tokens the interface carries. An empty secret
 *       makes one and keeps it in dataDir, so a restart does not sign every
 *       visitor out.
 * param: secret - the signing secret, or "" to load or generate one.
 * param: dataDir - where a generated secret is kept.
 * param: expiryHours - token lifetime; 24 if zero or less.
 * return: an Authenticator, or an error if the secret could not be established.
 */
func NewAuthenticator(secret, dataDir string, expiryHours int) (Authenticator, error) {
	svc, err := auth.NewJWTService(secret, dataDir, expiryHours)
	if err != nil {
		return nil, err
	}
	return kaijuAuth{svc: svc}, nil
}

/*
 * databaseOf unwraps a Store, tolerating nil.
 * desc: A nil Store and a Store holding nothing are the same absence to every
 *       caller, so both come back as a nil database rather than one of them
 *       panicking.
 * param: s - the store, or nil.
 * return: the database behind it, or nil.
 */
func databaseOf(s Store) *db.DB {
	if s == nil {
		return nil
	}
	return s.database()
}
