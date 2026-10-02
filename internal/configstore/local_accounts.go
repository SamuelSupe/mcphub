package configstore

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"golang.org/x/crypto/argon2"
)

var ErrCredentials = errors.New("invalid credentials or account unavailable")
var ErrLoginLimited = errors.New("too many login attempts; retry in five minutes")
var accountName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{2,63}$`)

type localAccount struct {
	Salt, Hash        []byte
	TOTP, PendingTOTP []byte
	PendingExpires    time.Time
	LastCounter       int64
	Failures          int
	LockedUntil       time.Time
}

type LocalAccountStatus struct {
	MFAEnabled bool `json:"mfa_enabled"`
}

func passwordHash(password string, salt []byte) []byte {
	return argon2.IDKey([]byte(password), salt, 2, 19*1024, 1, 32)
}

func validatePassword(password string) error {
	if !utf8.ValidString(password) || utf8.RuneCountInString(password) < 12 || len(password) > 1024 {
		return errors.New("password must contain at least 12 characters and at most 1024 bytes")
	}
	return nil
}

func newPassword(password string) (localAccount, error) {
	if err := validatePassword(password); err != nil {
		return localAccount{}, err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return localAccount{}, err
	}
	return localAccount{Salt: salt, Hash: passwordHash(password, salt), LastCounter: -1}, nil
}

func readLocalAccount(ctx context.Context, db identityReader, id string) (localAccount, error) {
	var data []byte
	err := db.QueryRowContext(ctx, "SELECT data FROM local_accounts WHERE identity_id=?", id).Scan(&data)
	var account localAccount
	if err == nil {
		err = json.Unmarshal(data, &account)
	}
	return account, err
}

func saveLocalAccount(ctx context.Context, tx *transaction, id string, account localAccount) error {
	data, err := json.Marshal(account)
	if err == nil {
		_, err = tx.ExecContext(ctx, "INSERT INTO local_accounts(identity_id,data) VALUES(?,?) ON CONFLICT(identity_id) DO UPDATE SET data=excluded.data", id, data)
	}
	return err
}

// Initialization is a local CLI operation. The durable marker prevents a second
// process from creating another bootstrap administrator, including after disable.
func (s *Store) CreateLocalAccount(ctx context.Context, username, name, password string, initialize bool) (Identity, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if !accountName.MatchString(username) || len(name) > 512 {
		return Identity{}, errors.New("username must be 3–64 ASCII letters, digits, dots, underscores or hyphens")
	}
	account, err := newPassword(password)
	if err != nil {
		return Identity{}, err
	}
	s.identityMu.Lock()
	defer s.identityMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Identity{}, err
	}
	defer tx.Rollback()
	if initialize {
		result, err := tx.ExecContext(ctx, "INSERT INTO metadata(key,value) VALUES('local_admin_initialized',?) ON CONFLICT(key) DO NOTHING", []byte("1"))
		if err != nil {
			return Identity{}, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return Identity{}, err
		}
		if n != 1 {
			return Identity{}, errors.New("administrator already initialized")
		}
	} else {
		var marker []byte
		if err := tx.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key='local_admin_initialized'").Scan(&marker); err != nil {
			return Identity{}, errors.New("initialize the administrator locally first")
		}
	}
	p, err := ensureIdentity(ctx, tx, config.LocalIdentityProvider, "user", username)
	if err != nil {
		return p, err
	}
	if p.Revision != 0 {
		return p, ErrConflict
	}
	p.Enabled, p.Name, p.CredentialVersion = true, name, 1
	if p.Name == "" {
		p.Name = username
	}
	if initialize {
		group, err := administratorGroup(ctx, tx, config.LocalIdentityProvider)
		if err != nil {
			return p, err
		}
		p.Groups = []string{group.ID}
	}
	p, err = saveIdentity(ctx, tx, p, "local_account_created")
	if err == nil {
		err = saveLocalAccount(ctx, tx, p.ID, account)
	}
	if err == nil {
		err = tx.Commit()
	}
	return p, err
}

func (s *Store) LocalInitialized(ctx context.Context) bool {
	var marker []byte
	return s.db.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key='local_admin_initialized'").Scan(&marker) == nil
}

func (s *Store) LocalAccountStatus(ctx context.Context, id string) (LocalAccountStatus, error) {
	account, err := readLocalAccount(ctx, s.db, id)
	return LocalAccountStatus{MFAEnabled: len(account.TOTP) > 0}, err
}

func (s *Store) AuthenticateLocal(ctx context.Context, username, password, code string, requireMFA bool) (Identity, bool, error) {
	if len(password) > 1024 || len(username) > 64 || len(code) > 6 {
		return Identity{}, false, ErrCredentials
	}
	username = strings.ToLower(strings.TrimSpace(username))
	p, err := readIdentity(ctx, s.db, "provider=? AND kind='user' AND external_id=?", config.LocalIdentityProvider, username)
	if errors.Is(err, ErrNotFound) {
		// A missing account still pays the password hashing cost.
		passwordHash(password, make([]byte, 16))
		return Identity{}, false, ErrCredentials
	}
	if err != nil {
		return p, false, err
	}
	account, err := readLocalAccount(ctx, s.db, p.ID)
	if err != nil {
		return p, false, err
	}
	if time.Now().Before(account.LockedUntil) {
		return Identity{}, false, ErrLoginLimited
	}
	hash := passwordHash(password, account.Salt)
	// Hash outside admission/database locks; re-read credentials and counters so a
	// concurrent reset, disable, lockout or TOTP replay still fails closed.
	s.identityMu.Lock()
	defer s.identityMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Identity{}, false, err
	}
	defer tx.Rollback()
	p, err = readIdentity(ctx, tx, "id=?", p.ID)
	if err != nil {
		return Identity{}, false, err
	}
	account, err = readLocalAccount(ctx, tx, p.ID)
	if err != nil {
		return p, false, err
	}
	if time.Now().Before(account.LockedUntil) {
		return Identity{}, false, ErrLoginLimited
	}
	ok := subtle.ConstantTimeCompare(hash, account.Hash) == 1 && p.Enabled && p.DirectoryActive
	mfa := len(account.TOTP) > 0
	if ok && mfa {
		secret, err := s.open("totp:"+p.ID, account.TOTP)
		if err != nil {
			return p, false, err
		}
		counter, valid := verifyTOTP(secret, code, time.Now(), account.LastCounter)
		ok = valid
		if valid {
			account.LastCounter = counter
		}
	} else if requireMFA {
		ok = false
	}
	if !ok {
		account.Failures++
		if account.Failures >= 5 {
			account.LockedUntil = time.Now().Add(5 * time.Minute)
			account.Failures = 0
		}
	} else {
		account.Failures = 0
		account.LockedUntil = time.Time{}
	}
	if err := saveLocalAccount(ctx, tx, p.ID, account); err != nil {
		return p, false, err
	}
	if err := tx.Commit(); err != nil {
		return p, false, err
	}
	if !ok {
		return Identity{}, false, ErrCredentials
	}
	return p, mfa, nil
}

func (s *Store) SetLocalPassword(ctx context.Context, id, password string, expectedVersion int64, resetMFA bool) error {
	newAccount, err := newPassword(password)
	if err != nil {
		return err
	}
	s.identityMu.Lock()
	defer s.identityMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := readIdentity(ctx, tx, "id=?", id)
	if err != nil {
		return err
	}
	if p.Provider != config.LocalIdentityProvider || (expectedVersion != 0 && p.CredentialVersion != expectedVersion) {
		return ErrIdentityDenied
	}
	account, err := readLocalAccount(ctx, tx, id)
	if err != nil {
		return err
	}
	account.Salt, account.Hash, account.Failures, account.LockedUntil = newAccount.Salt, newAccount.Hash, 0, time.Time{}
	account.PendingTOTP = nil
	if resetMFA {
		account.TOTP = nil
		account.LastCounter = -1
	}
	p.CredentialVersion++
	if _, err = saveIdentity(ctx, tx, p, "local_password_changed"); err == nil {
		err = saveLocalAccount(ctx, tx, id, account)
	}
	if err == nil {
		err = revokeIdentitySessions(ctx, tx, id)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err == nil {
		s.cancelIdentityCalls(ctx)
	}
	return err
}

func (s *Store) BeginLocalMFA(ctx context.Context, id string, version int64) (string, error) {
	s.identityMu.Lock()
	defer s.identityMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	p, err := readIdentity(ctx, tx, "id=?", id)
	if err != nil || p.CredentialVersion != version || !p.Enabled {
		return "", ErrIdentityDenied
	}
	account, err := readLocalAccount(ctx, tx, id)
	if err != nil {
		return "", err
	}
	if len(account.TOTP) != 0 {
		return "", errors.New("MFA is already enabled")
	}
	secret := make([]byte, 20)
	if _, err = rand.Read(secret); err != nil {
		return "", err
	}
	account.PendingTOTP, err = s.seal("totp:"+id, secret)
	if err != nil {
		return "", err
	}
	account.PendingExpires = time.Now().Add(5 * time.Minute)
	if err = saveLocalAccount(ctx, tx, id, account); err == nil {
		err = tx.Commit()
	}
	return encodeTOTPSecret(secret), err
}

func (s *Store) ConfirmLocalMFA(ctx context.Context, id, code string, version int64) error {
	s.identityMu.Lock()
	defer s.identityMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := readIdentity(ctx, tx, "id=?", id)
	if err != nil || p.CredentialVersion != version || !p.Enabled {
		return ErrIdentityDenied
	}
	account, err := readLocalAccount(ctx, tx, id)
	if err != nil {
		return err
	}
	if len(account.PendingTOTP) == 0 || !time.Now().Before(account.PendingExpires) || len(account.TOTP) != 0 {
		return ErrCredentials
	}
	secret, err := s.open("totp:"+id, account.PendingTOTP)
	if err != nil {
		return err
	}
	counter, ok := verifyTOTP(secret, code, time.Now(), -1)
	if !ok {
		return ErrCredentials
	}
	account.TOTP, account.PendingTOTP, account.LastCounter = account.PendingTOTP, nil, counter
	p.CredentialVersion++
	if _, err = saveIdentity(ctx, tx, p, "local_mfa_enabled"); err == nil {
		err = saveLocalAccount(ctx, tx, id, account)
	}
	if err == nil {
		err = revokeIdentitySessions(ctx, tx, id)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err == nil {
		s.cancelIdentityCalls(ctx)
	}
	return err
}
