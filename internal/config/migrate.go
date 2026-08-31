package config

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

var ErrMigrationRequired = errors.New("MLink config migration is required")

type MigrationPreview struct {
	FromVersion          int    `json:"from_version"`
	ToVersion            int    `json:"to_version"`
	OwnerCanonicalUserID string `json:"owner_canonical_user_id"`
}

func PreviewV1Migration(data []byte) (MigrationPreview, error) {
	var legacy struct {
		SchemaVersion      int    `yaml:"schema_version"`
		ActiveConnectionID string `yaml:"active_connection_id"`
		Connections        map[string]struct {
			UserID string `yaml:"user_id"`
		} `yaml:"connections"`
	}
	if err := yaml.Unmarshal(data, &legacy); err != nil {
		return MigrationPreview{}, fmt.Errorf("decode v1 MLink config: %w", err)
	}
	if legacy.SchemaVersion != 1 {
		return MigrationPreview{}, errors.New("MLink config is not schema v1")
	}
	connection, exists := legacy.Connections[legacy.ActiveConnectionID]
	if !exists || strings.TrimSpace(connection.UserID) == "" {
		return MigrationPreview{}, errors.New("v1 owner user identity is missing")
	}
	slug := stableOwnerSlug(connection.UserID)
	return MigrationPreview{FromVersion: 1, ToVersion: 2, OwnerCanonicalUserID: "usr_owner_" + slug}, nil
}

var nonOwnerSlug = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func stableOwnerSlug(value string) string {
	value = strings.Trim(nonOwnerSlug.ReplaceAllString(strings.TrimSpace(value), "-"), "-")
	if value == "" {
		return "local"
	}
	return value
}
