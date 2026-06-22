package server

import (
	"context"
	"database/sql"
	"fmt"

	"p2pstream/internal/secrets"
)

type databaseSecretRow struct {
	id            int64
	ownerID       int64
	legacyOwnerID sql.NullInt64
	value         string
}

type databaseSecretMigration struct {
	name    string
	purpose secrets.Purpose
	selectQ string
	updateQ string
}

func (a *App) migrateDatabaseSecrets(ctx context.Context) (int, error) {
	if a == nil || a.DB == nil {
		return 0, nil
	}
	migrations := []databaseSecretMigration{
		{
			name:    "public route target basic auth password",
			purpose: secrets.PurposePublicRouteTargetBasicAuthPassword,
			selectQ: `SELECT id, id, NULL, upstream_basic_auth_password
				FROM public_route_targets
				WHERE upstream_basic_auth_password <> ''`,
			updateQ: `UPDATE public_route_targets
				SET upstream_basic_auth_password = ?, updated_at = CURRENT_TIMESTAMP
				WHERE id = ?`,
		},
		{
			name:    "public route target sensitive upstream header",
			purpose: secrets.PurposePublicRouteTargetSensitiveHeader,
			selectQ: `SELECT id, id, target_id, value
				FROM public_route_target_upstream_headers
				WHERE value <> ''
				  AND (sensitive <> 0 OR lower(name) IN ('authorization', 'proxy-authorization', 'cookie'))`,
			updateQ: `UPDATE public_route_target_upstream_headers
				SET value = ?, updated_at = CURRENT_TIMESTAMP
				WHERE id = ?`,
		},
		{
			name:    "TLS DNS credential API token",
			purpose: secrets.PurposePublicTLSDNSCredentialAPIToken,
			selectQ: `SELECT id, id, NULL, api_token
				FROM public_tls_dns_credentials
				WHERE api_token <> ''`,
			updateQ: `UPDATE public_tls_dns_credentials
				SET api_token = ?, updated_at = CURRENT_TIMESTAMP
				WHERE id = ?`,
		},
		{
			name:    "WAF captcha provider secret key",
			purpose: secrets.PurposePublicWAFCaptchaProviderSecretKey,
			selectQ: `SELECT id, id, NULL, secret_key
				FROM public_waf_captcha_providers
				WHERE secret_key <> ''`,
			updateQ: `UPDATE public_waf_captcha_providers
				SET secret_key = ?, updated_at = CURRENT_TIMESTAMP
				WHERE id = ?`,
		},
		{
			name:    "WAF cookie signing secret",
			purpose: secrets.PurposePublicWAFCookieSigningSecret,
			selectQ: `SELECT id, id, NULL, cookie_signing_secret
				FROM public_waf_settings
				WHERE cookie_signing_secret <> ''`,
			updateQ: `UPDATE public_waf_settings
				SET cookie_signing_secret = ?, updated_at = CURRENT_TIMESTAMP
				WHERE id = ?`,
		},
		{
			name:    "environment access token",
			purpose: secrets.PurposeEnvironmentAccessToken,
			selectQ: `SELECT id, id, NULL, access_token
				FROM environments
				WHERE access_token <> ''`,
			updateQ: `UPDATE environments
				SET access_token = ?, updated_at = CURRENT_TIMESTAMP
				WHERE id = ?`,
		},
	}

	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin database secret migration: %w", err)
	}
	defer tx.Rollback()

	migrated := 0
	for _, migration := range migrations {
		count, err := a.migrateDatabaseSecretRows(ctx, tx, migration)
		if err != nil {
			return 0, err
		}
		migrated += count
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit database secret migration: %w", err)
	}
	return migrated, nil
}

func (a *App) migrateDatabaseSecretRows(ctx context.Context, tx *sql.Tx, migration databaseSecretMigration) (int, error) {
	rows, err := tx.QueryContext(ctx, migration.selectQ)
	if err != nil {
		return 0, fmt.Errorf("scan %s: %w", migration.name, err)
	}
	defer rows.Close()

	candidates := make([]databaseSecretRow, 0)
	for rows.Next() {
		var row databaseSecretRow
		if err := rows.Scan(&row.id, &row.ownerID, &row.legacyOwnerID, &row.value); err != nil {
			return 0, fmt.Errorf("scan %s row: %w", migration.name, err)
		}
		candidates = append(candidates, row)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("scan %s rows: %w", migration.name, err)
	}

	migrated := 0
	for _, row := range candidates {
		if secrets.IsEncrypted(row.value) {
			if a.Secrets == nil || !a.Secrets.Enabled() {
				return 0, fmt.Errorf("%s %d is encrypted but SECRETS_ENCRYPTION_KEY is not configured", migration.name, row.id)
			}
			plaintext, legacyOwner, err := a.decryptMigratedSecret(migration, row)
			if err != nil {
				return 0, fmt.Errorf("decrypt %s %d: %w", migration.name, row.id, err)
			}
			if legacyOwner || a.Secrets.NeedsRewrap(row.value) {
				encrypted, err := a.encryptSecret(migration.purpose, row.ownerID, plaintext)
				if err != nil {
					return 0, fmt.Errorf("rewrap %s %d: %w", migration.name, row.id, err)
				}
				if _, err := tx.ExecContext(ctx, migration.updateQ, encrypted, row.id); err != nil {
					return 0, fmt.Errorf("update %s %d: %w", migration.name, row.id, err)
				}
				migrated++
			}
			continue
		}
		if a.Secrets != nil && a.Secrets.Required() {
			return 0, fmt.Errorf("%s %d is plaintext but secrets encryption is required", migration.name, row.id)
		}
		if a.Secrets == nil || !a.Secrets.Enabled() {
			continue
		}
		encrypted, err := a.encryptSecret(migration.purpose, row.ownerID, row.value)
		if err != nil {
			return 0, fmt.Errorf("encrypt %s %d: %w", migration.name, row.id, err)
		}
		if _, err := tx.ExecContext(ctx, migration.updateQ, encrypted, row.id); err != nil {
			return 0, fmt.Errorf("update %s %d: %w", migration.name, row.id, err)
		}
		migrated++
	}
	return migrated, nil
}

func (a *App) decryptMigratedSecret(migration databaseSecretMigration, row databaseSecretRow) (string, bool, error) {
	plaintext, _, err := a.decryptSecret(migration.purpose, row.ownerID, row.value)
	if err == nil {
		return plaintext, false, nil
	}
	if !row.legacyOwnerID.Valid || row.legacyOwnerID.Int64 == row.ownerID {
		return "", false, err
	}
	plaintext, _, legacyErr := a.decryptSecret(migration.purpose, row.legacyOwnerID.Int64, row.value)
	if legacyErr != nil {
		return "", false, err
	}
	return plaintext, true, nil
}
