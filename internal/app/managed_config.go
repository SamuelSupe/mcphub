package app

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
	"github.com/SamuelSupe/mcphub/v2/internal/httptool"
)

func prepareManagedConfig(ctx context.Context, static *config.Config, configPath string) (*config.Config, *configstore.Store, error) {
	if !static.Admin.Enabled {
		return static, nil, nil
	}
	key, err := config.AdminEncryptionKey(static.Admin)
	if err != nil {
		return nil, nil, err
	}
	store, err := openConfigStore(ctx, static.Admin, key, false)
	if err != nil {
		return nil, nil, err
	}
	initialized, err := store.Initialized(ctx)
	if err != nil {
		_ = store.Close()
		return nil, nil, err
	}
	if !initialized {
		bootstrap, err := config.Load(configPath)
		if err != nil {
			_ = store.Close()
			return nil, nil, fmt.Errorf("load bootstrap backends: %w", err)
		}
		if err := store.Bootstrap(ctx, bootstrap.Backends); err != nil {
			_ = store.Close()
			return nil, nil, err
		}
	}
	records, err := store.List(ctx)
	if err != nil {
		_ = store.Close()
		return nil, nil, err
	}
	managed := configWithRecords(static, records)
	if err := managed.Validate(); err != nil {
		_ = store.Close()
		return nil, nil, fmt.Errorf("validate stored backends: %w", err)
	}
	return managed, store, nil
}

func ValidateConfig(ctx context.Context, configPath string) error {
	static, err := config.LoadStatic(configPath)
	if err != nil {
		return err
	}
	if !static.Admin.Enabled {
		return nil
	}
	if static.Admin.Driver() == "sqlite" {
		if _, err := os.Stat(static.Admin.DatabasePath); errors.Is(err, os.ErrNotExist) {
			_, err := config.Load(configPath)
			return err
		} else if err != nil {
			return fmt.Errorf("inspect configuration database: %w", err)
		}
	}
	key, err := config.AdminEncryptionKey(static.Admin)
	if err != nil {
		return err
	}
	store, err := openConfigStore(ctx, static.Admin, key, true)
	if errors.Is(err, configstore.ErrUninitialized) {
		_, err := config.Load(configPath)
		return err
	}
	if err != nil {
		return err
	}
	defer store.Close()
	initialized, err := store.Initialized(ctx)
	if err != nil {
		return err
	}
	if !initialized {
		_, err := config.Load(configPath)
		return err
	}
	records, err := store.List(ctx)
	if err != nil {
		return err
	}
	managed := configWithRecords(static, records)
	if err := managed.Validate(); err != nil {
		return err
	}
	groupRecords, err := store.ListToolGroups(ctx)
	if err != nil {
		return err
	}
	groups := make([]httptool.GroupConfig, len(groupRecords))
	for index, record := range groupRecords {
		groups[index] = record.Config
	}
	return httptool.ValidateGroups(groups, backendIDs(managed))
}

func configWithRecords(base *config.Config, records []configstore.Record) *config.Config {
	result := *base
	result.Server.AllowedOrigins = slices.Clone(base.Server.AllowedOrigins)
	result.Backends = make([]config.BackendConfig, 0, len(records))
	for _, record := range records {
		if !record.Enabled {
			continue
		}
		result.Backends = append(result.Backends, cloneBackendConfig(record.Config))
	}
	return &result
}

func cloneBackendConfig(value config.BackendConfig) config.BackendConfig {
	result := value
	result.Credentials = config.CloneCredentials(value.Credentials)
	result.RequiredScopes = slices.Clone(value.RequiredScopes)
	result.PublishedTools = slices.Clone(value.PublishedTools)
	result.Headers = maps.Clone(value.Headers)
	result.ToolRules = config.CloneToolRules(value.ToolRules)
	if value.OAuth != nil {
		oauth := *value.OAuth
		oauth.Scopes = slices.Clone(value.OAuth.Scopes)
		result.OAuth = &oauth
	}
	return result
}

func openConfigStore(ctx context.Context, cfg config.AdminConfig, key []byte, readOnly bool) (*configstore.Store, error) {
	if cfg.Driver() == "postgres" {
		return configstore.OpenPostgres(ctx, os.Getenv(cfg.DatabaseDSNEnv), key, readOnly)
	}
	if readOnly {
		return configstore.OpenReadOnly(ctx, cfg.DatabasePath, key)
	}
	return configstore.Open(ctx, cfg.DatabasePath, key)
}
