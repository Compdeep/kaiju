package db

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Browser pairing.
//
// Two credentials, kept apart on purpose. A pair code is what a person copies
// out of the chat and pastes into the extension: short-lived, single-use, and
// likely to sit in a transcript afterwards. On first contact the extension
// trades it for a device secret that only the extension holds. So the code that
// was shown is worth nothing once it has been used, and the secret that matters
// was never shown to anyone.
//
// Only hashes are stored. Both values are long random strings, so a plain
// SHA-256 is enough — there is no low-entropy password here for a slow hash to
// protect. The hashing is the caller's; this file stores what it is given.

// ErrPairCodeInvalid is a code that is unknown, already used or expired. One
// error for all three, so a guesser learns nothing about which.
var ErrPairCodeInvalid = errors.New("pair code is invalid, used or expired")

// ErrBrowserDeviceUnknown is a device id and secret that do not match a live
// device: never paired, revoked, or the wrong secret.
var ErrBrowserDeviceUnknown = errors.New("browser device is unknown or revoked")

/*
 * BrowserDevice is a browser that has been paired.
 * desc: One row per extension install. Username is who paired it, and the
 *       only person whose runs may drive it.
 */
type BrowserDevice struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	Name      string `json:"name"`
	CreatedAt int64  `json:"created_at"`
	LastSeen  int64  `json:"last_seen"`
	RevokedAt int64  `json:"revoked_at,omitempty"`
}

/*
 * CreateBrowserPairCode records a new pair code.
 * param: codeHash - the hash of the code shown to the person.
 * param: username - who asked for it; empty on a node with no sign-in.
 * param: expires - when it stops working if nobody uses it.
 * return: error if the insert fails.
 */
func (d *DB) CreateBrowserPairCode(codeHash, username string, expires time.Time) error {
	_, err := d.conn.Exec(
		`INSERT INTO browser_pair_codes (code_hash, username, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		codeHash, username, time.Now().Unix(), expires.Unix(),
	)
	if err != nil {
		return fmt.Errorf("db: create browser pair code: %w", err)
	}
	return nil
}

/*
 * ClaimBrowserPairCode uses up a pair code and creates the device it pairs.
 * desc: One transaction, so a code cannot pair two devices however close
 *       together the two attempts arrive.
 * param: codeHash - the hash of the code the extension sent.
 * param: dev - the device to create; Username is filled from the code.
 * param: secretHash - the hash of the secret the device will hold.
 * param: now - the current time.
 * return: the device as stored, or ErrPairCodeInvalid.
 */
func (d *DB) ClaimBrowserPairCode(codeHash string, dev BrowserDevice, secretHash string, now time.Time) (*BrowserDevice, error) {
	tx, err := d.conn.Begin()
	if err != nil {
		return nil, fmt.Errorf("db: claim pair code: %w", err)
	}
	defer tx.Rollback()

	var username string
	var expires, used int64
	err = tx.QueryRow(
		`SELECT username, expires_at, used_at FROM browser_pair_codes WHERE code_hash = ?`, codeHash,
	).Scan(&username, &expires, &used)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (used != 0 || now.Unix() > expires)) {
		return nil, ErrPairCodeInvalid
	}
	if err != nil {
		return nil, fmt.Errorf("db: claim pair code: %w", err)
	}
	if _, err := tx.Exec(`UPDATE browser_pair_codes SET used_at = ? WHERE code_hash = ?`, now.Unix(), codeHash); err != nil {
		return nil, fmt.Errorf("db: claim pair code: %w", err)
	}
	dev.Username = username
	dev.CreatedAt = now.Unix()
	dev.LastSeen = now.Unix()
	if _, err := tx.Exec(
		`INSERT INTO browser_devices (id, username, name, secret_hash, created_at, last_seen) VALUES (?, ?, ?, ?, ?, ?)`,
		dev.ID, dev.Username, dev.Name, secretHash, dev.CreatedAt, dev.LastSeen,
	); err != nil {
		return nil, fmt.Errorf("db: create browser device: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("db: claim pair code: %w", err)
	}
	return &dev, nil
}

/*
 * BrowserDeviceBySecret returns a live device whose secret matches.
 * param: id - the device id the extension sent.
 * param: secretHash - the hash of the secret it sent.
 * return: the device, or ErrBrowserDeviceUnknown.
 */
func (d *DB) BrowserDeviceBySecret(id, secretHash string) (*BrowserDevice, error) {
	var dev BrowserDevice
	err := d.conn.QueryRow(
		`SELECT id, username, name, created_at, last_seen, revoked_at FROM browser_devices
		 WHERE id = ? AND secret_hash = ? AND revoked_at = 0`, id, secretHash,
	).Scan(&dev.ID, &dev.Username, &dev.Name, &dev.CreatedAt, &dev.LastSeen, &dev.RevokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrBrowserDeviceUnknown
	}
	if err != nil {
		return nil, fmt.Errorf("db: browser device: %w", err)
	}
	return &dev, nil
}

/*
 * TouchBrowserDevice records that a device was just seen, and its name.
 * param: id - the device.
 * param: name - the name the extension reported this time.
 * param: now - the current time.
 * return: error if the update fails.
 */
func (d *DB) TouchBrowserDevice(id, name string, now time.Time) error {
	_, err := d.conn.Exec(`UPDATE browser_devices SET last_seen = ?, name = ? WHERE id = ?`, now.Unix(), name, id)
	if err != nil {
		return fmt.Errorf("db: touch browser device: %w", err)
	}
	return nil
}

/*
 * ListBrowserDevices returns the live devices one person has paired.
 * param: username - whose devices.
 * return: the devices, most recently seen first.
 */
func (d *DB) ListBrowserDevices(username string) ([]BrowserDevice, error) {
	rows, err := d.conn.Query(
		`SELECT id, username, name, created_at, last_seen, revoked_at FROM browser_devices
		 WHERE username = ? AND revoked_at = 0 ORDER BY last_seen DESC`, username,
	)
	if err != nil {
		return nil, fmt.Errorf("db: list browser devices: %w", err)
	}
	defer rows.Close()
	var out []BrowserDevice
	for rows.Next() {
		var dev BrowserDevice
		if err := rows.Scan(&dev.ID, &dev.Username, &dev.Name, &dev.CreatedAt, &dev.LastSeen, &dev.RevokedAt); err != nil {
			return nil, err
		}
		out = append(out, dev)
	}
	return out, rows.Err()
}

/*
 * RevokeBrowserDevice stops a device from connecting again.
 * param: id - the device.
 * param: username - who is revoking it; only its owner may.
 * return: ErrBrowserDeviceUnknown when there is no such live device of theirs.
 */
func (d *DB) RevokeBrowserDevice(id, username string) error {
	res, err := d.conn.Exec(
		`UPDATE browser_devices SET revoked_at = ? WHERE id = ? AND username = ? AND revoked_at = 0`,
		time.Now().Unix(), id, username,
	)
	if err != nil {
		return fmt.Errorf("db: revoke browser device: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrBrowserDeviceUnknown
	}
	return nil
}
