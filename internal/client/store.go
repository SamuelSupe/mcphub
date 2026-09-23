package client

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/sys/unix"
)

var (
	ErrLoginRequired  = errors.New("login required; run mcphub-cli login for this profile")
	ErrProfileChanged = errors.New("profile was logged in again; restart the MCP connection")
	profileName       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
)

type Store struct{ Dir string }

type profile struct {
	Version   int           `json:"version"`
	Session   string        `json:"session"`
	ServerURL string        `json:"server_url"`
	Issuer    string        `json:"issuer"`
	ClientID  string        `json:"client_id"`
	TokenURL  string        `json:"token_url"`
	Scopes    []string      `json:"scopes"`
	Token     *oauth2.Token `json:"token,omitempty"`
}

// Status describes cached credentials, without checking their validity at the issuer.
type Status struct {
	ServerURL  string
	ClientID   string
	Scopes     []string
	LoggedIn   bool
	ExpiresAt  time.Time
	CanRefresh bool
}

func DefaultStore() (*Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return &Store{Dir: filepath.Join(home, ".mcphub")}, nil
}

func (s *Store) Status(ctx context.Context, name string) (Status, error) {
	var status Status
	err := s.locked(ctx, name, func() error {
		p, err := s.load(name)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		status = Status{ServerURL: p.ServerURL, ClientID: p.ClientID, Scopes: p.Scopes}
		if p.Token != nil {
			status.LoggedIn = true
			status.ExpiresAt = p.Token.Expiry
			status.CanRefresh = p.Token.RefreshToken != ""
		}
		return nil
	})
	return status, err
}

func (s *Store) Logout(ctx context.Context, name string) error {
	return s.locked(ctx, name, func() error {
		p, err := s.load(name)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		p.Token = nil
		p.Session = rand.Text()
		return s.save(name, p)
	})
}

func (s *Store) locked(ctx context.Context, name string, fn func() error) error {
	if !profileName.MatchString(name) {
		return errors.New("profile must contain 1-64 letters, digits, underscores or hyphens, starting with a letter or digit")
	}
	if err := os.MkdirAll(s.Dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(s.Dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("credential directory must be a real directory accessible only by its owner (0700)")
	}
	lock, err := privateFile(filepath.Join(s.Dir, name+".lock"), unix.O_RDWR|unix.O_CREAT)
	if err != nil {
		return err
	}
	defer lock.Close()
	ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	for {
		err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if err != unix.EWOULDBLOCK && err != unix.EAGAIN {
			return err
		}
		select {
		case <-ctx.Done():
			return errors.New("waiting for credential lock was canceled or timed out")
		case <-time.After(20 * time.Millisecond):
		}
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn()
}

func privateFile(path string, flags int) (*os.File, error) {
	fd, err := unix.Open(path, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	var stat unix.Stat_t
	err = unix.Fstat(fd, &stat)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || stat.Uid != uint32(os.Geteuid()) {
		f.Close()
		return nil, errors.New("credential files must be regular files owned by the current user with permissions 0600")
	}
	return f, nil
}

func (s *Store) load(name string) (*profile, error) {
	f, err := privateFile(filepath.Join(s.Dir, name+".json"), unix.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	var p profile
	if len(data) > 1<<20 || json.Unmarshal(data, &p) != nil || p.Version != 1 || p.Session == "" || p.ClientID == "" {
		return nil, errors.New("invalid credential file; log in again with --server and --client-id")
	}
	for _, raw := range []string{p.ServerURL, p.Issuer, p.TokenURL} {
		if _, err := httpsURL(raw); err != nil {
			return nil, errors.New("invalid endpoint in credential file")
		}
	}
	return &p, nil
}

// The stable lock file is never replaced: all processes must lock the same inode.
func (s *Store) save(name string, p *profile) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.Dir, ".credentials-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), filepath.Join(s.Dir, name+".json")); err != nil {
		return err
	}
	dir, err := os.Open(s.Dir)
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync credential directory: %w", err)
	}
	return nil
}
