package db

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

//go:embed schema_cutover.sql
var schemaCutoverSQL string

// applySchemaCutover retires inert cache fields and historical rollup state.
// Existing raw history must have completed its prior-release backfill first.
func applySchemaCutover(ctx context.Context, tx *sql.Tx) error {
	var complete bool
	if err := tx.QueryRowContext(ctx, `SELECT
		proxy_backfilled_through_id >= proxy_backfill_upper_id AND
		agent_backfilled_through_id >= agent_backfill_upper_id
		FROM observability_rollup_state WHERE id = 1`).Scan(&complete); err != nil {
		return fmt.Errorf("inspect observability backfill before schema cutover: %w", err)
	}
	if !complete {
		return errors.New("unsupported unfinished observability backfill: complete it using the previous release before upgrading")
	}
	if err := validateSchemaCutoverConfiguration(ctx, tx); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, schemaCutoverSQL)
	return err
}

// These checks freeze the schema-18 support boundary. Reject retired stored
// shapes before changing the schema so operators can resave them with the
// previous release; do not reconstruct policy meaning or rewrite origins.
func validateSchemaCutoverConfiguration(ctx context.Context, tx *sql.Tx) error {
	for _, table := range []string{"public_rate_limit_rules", "public_traffic_shaper_rules", "public_waf_rules", "public_cache_rules", "public_retry_rules"} {
		var id int64
		err := tx.QueryRowContext(ctx, `SELECT rules.id FROM `+table+` AS rules, json_tree(CASE WHEN trim(rules.match_json, char(9) || char(10) || char(13) || ' ') = '' THEN '{}' ELSE rules.match_json END) AS field WHERE field.key = 'legacy_first_value' LIMIT 1`).Scan(&id)
		if err == nil {
			return fmt.Errorf("unsupported policy in %s row %d: resave legacy_first_value as equivalent CEL using the previous release before upgrading", table, id)
		}
		if err != sql.ErrNoRows {
			return fmt.Errorf("inspect %s before schema cutover: %w", table, err)
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, url, target_type FROM public_route_targets`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var raw, targetType string
		if err := rows.Scan(&id, &raw, &targetType); err != nil {
			return err
		}
		if strings.EqualFold(strings.TrimSpace(targetType), "static") {
			continue
		}
		origin, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || (origin.Scheme != "http" && origin.Scheme != "https") || origin.Host == "" || origin.User != nil || origin.Opaque != "" || origin.RawQuery != "" || origin.ForceQuery || origin.Fragment != "" || (origin.Path != "" && origin.Path != "/") || (origin.RawPath != "" && origin.RawPath != "/") {
			// Do not include the URL: unsupported old values can contain credentials.
			return fmt.Errorf("unsupported proxy target %d: save an HTTP(S) origin without credentials, path, query, or fragment using the previous release before upgrading", id)
		}
	}
	return rows.Err()
}
