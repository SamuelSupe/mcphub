package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
	"golang.org/x/term"
)

func initAdminCommand(args []string) error {
	flags := flag.NewFlagSet("init-admin", flag.ContinueOnError)
	path := flags.String("config", "", "configuration file")
	username := flags.String("username", "admin", "administrator username")
	stdin := flags.Bool("password-stdin", false, "read password from stdin (one line, at most 1024 bytes)")
	reset := flags.Bool("reset", false, "reset an existing local account instead of initializing")
	resetMFA := flags.Bool("reset-mfa", false, "clear MFA during local account recovery; requires --reset")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *path == "" || flags.NArg() != 0 {
		return fmt.Errorf("usage: mcphub init-admin --config PATH [--username admin] [--password-stdin]")
	}
	if *resetMFA && !*reset {
		return fmt.Errorf("--reset-mfa requires --reset")
	}
	cfg, err := config.LoadStatic(*path)
	if err != nil {
		return err
	}
	if !cfg.Auth.Builtin() {
		return fmt.Errorf("init-admin requires auth.mode: builtin")
	}
	var password []byte
	if *stdin {
		password, err = io.ReadAll(io.LimitReader(os.Stdin, 1027))
		password = []byte(strings.TrimSuffix(strings.TrimSuffix(string(password), "\n"), "\r"))
		if strings.ContainsAny(string(password), "\r\n") {
			return fmt.Errorf("password-stdin requires a single line")
		}
	} else {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return fmt.Errorf("use an interactive terminal or --password-stdin")
		}
		fmt.Fprint(os.Stderr, "Account password (at least 12 characters): ")
		password, err = term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return err
		}
		fmt.Fprint(os.Stderr, "Confirm password: ")
		confirm, e := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if e != nil {
			return e
		}
		if string(password) != string(confirm) {
			return fmt.Errorf("passwords do not match")
		}
	}
	if err != nil {
		return err
	}
	key, err := config.AdminEncryptionKey(cfg.Admin)
	if err != nil {
		return err
	}
	ctx := context.Background()
	var store *configstore.Store
	if cfg.Admin.DatabaseDriver == "postgres" {
		store, err = configstore.OpenPostgres(ctx, os.Getenv(cfg.Admin.DatabaseDSNEnv), key, false)
	} else {
		store, err = configstore.Open(ctx, cfg.Admin.DatabasePath, key)
	}
	if err != nil {
		return err
	}
	defer store.Close()
	if *reset {
		users, e := store.Identities(ctx)
		if e != nil {
			return e
		}
		id := ""
		for _, user := range users {
			if user.Kind == "user" && user.Provider == config.LocalIdentityProvider && user.ExternalID == strings.ToLower(strings.TrimSpace(*username)) {
				id = user.ID
				break
			}
		}
		if id == "" {
			return fmt.Errorf("local account not found")
		}
		err = store.SetLocalPassword(configstore.WithActor(ctx, "local-recovery"), id, string(password), 0, *resetMFA)
	} else {
		_, err = store.CreateLocalAccount(configstore.WithActor(ctx, "local-initialization"), *username, *username, string(password), true)
	}
	if err == nil {
		if *reset {
			fmt.Fprintln(os.Stdout, "Account password reset. Existing sessions revoked.")
		} else {
			fmt.Fprintln(os.Stdout, "Administrator initialized. Sign in to the management console.")
		}
	}
	return err
}
