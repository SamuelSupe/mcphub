package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
	"github.com/SamuelSupe/mcphub/v2/internal/version"
)

func backupCommand(command string, args []string) error {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	path := flags.String("config", "", "deployment configuration")
	directory := flags.String("backup", "", "backup directory")
	output := flags.String("output", "", "new backup directory")
	into := flags.String("into", "", "new SQLite database path, or DSN environment variable for PostgreSQL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *path == "" {
		return errors.New("--config is required; positional arguments are not accepted")
	}
	cfg, err := config.LoadStatic(*path)
	if err != nil {
		return err
	}
	if !cfg.Admin.Enabled {
		return errors.New("backup requires managed database storage")
	}
	key, err := config.AdminEncryptionKey(cfg.Admin)
	if err != nil {
		return err
	}
	keyID := configstore.EncryptionKeyID(key)
	if command == "restore" && (*directory == "" || *into == "") {
		return errors.New("restore requires --backup and --into (an empty destination)")
	}
	if command == "backup" && *output == "" {
		return errors.New("backup requires --output (a new directory)")
	}
	if command == "verify-backup" && *directory == "" {
		return errors.New("verify-backup requires --backup")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	open := func(destination string) (*configstore.Store, error) {
		if cfg.Admin.Driver() == "postgres" {
			if os.Getenv(destination) == "" {
				return nil, errors.New("database DSN environment variable is empty")
			}
			return configstore.OpenPostgres(ctx, os.Getenv(destination), key, false)
		}
		return configstore.Open(ctx, destination, key)
	}
	if command == "restore" {
		if cfg.Admin.Driver() == "sqlite" {
			if _, err = os.Lstat(*into); !errors.Is(err, os.ErrNotExist) {
				return errors.New("destination SQLite path already exists")
			}
		}
		if _, err = configstore.VerifyBackup(*directory, keyID); err != nil {
			return err
		}
		store, err := open(*into)
		if err != nil {
			return err
		}
		defer store.Close()
		manifest, err := store.RestoreBackup(ctx, *directory, keyID)
		if err != nil {
			return err
		}
		if err = store.RecordOperation(ctx, "recovery", map[string]any{"at": time.Now().UTC(), "backup": manifest, "security_revoked": true}); err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, "backup restored into empty database; previous sessions, grants and unfinished approvals revoked")
		return nil
	}
	source := cfg.Admin.DatabasePath
	if cfg.Admin.Driver() == "postgres" {
		source = cfg.Admin.DatabaseDSNEnv
	} else {
		if _, err = os.Stat(source); err != nil {
			return err
		}
	}
	store, err := open(source)
	if err != nil {
		return err
	}
	defer store.Close()
	if command == "verify-backup" {
		manifest, err := configstore.VerifyBackup(*directory, keyID)
		if err != nil {
			return err
		}
		// A recovery drill must restore and read the database, not just hash a file.
		if cfg.Admin.Driver() == "postgres" {
			if *into == "" {
				return errors.New("PostgreSQL recovery drill requires --into DSN_ENV for a dedicated empty database")
			}
		}
		temporary, err := os.MkdirTemp("", "mcphub-recovery-drill-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(temporary)
		var target *configstore.Store
		if cfg.Admin.Driver() == "postgres" {
			target, err = open(*into)
		} else {
			target, err = configstore.Open(ctx, filepath.Join(temporary, "config.db"), key)
		}
		if err != nil {
			return err
		}
		defer target.Close()
		if _, err = target.RestoreBackup(ctx, *directory, keyID); err != nil {
			return err
		}
		if _, err = target.List(ctx); err != nil {
			return err
		}
		if _, err = target.Identities(ctx); err != nil {
			return err
		}
		if err = store.RecordOperation(ctx, "recovery_drill", map[string]any{"at": time.Now().UTC(), "backup_sha256": manifest.SHA256, "result": "passed"}); err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, "backup checksum, encryption key and isolated database restore verified")
		return nil
	}
	raw, err := os.ReadFile(*path)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(raw)
	manifest, err := store.Backup(ctx, *output, version.Value, keyID, hex.EncodeToString(digest[:]))
	if err != nil {
		return err
	}
	if err = store.RecordOperation(ctx, "backup", manifest); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(manifest)
}
