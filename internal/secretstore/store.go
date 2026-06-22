package secretstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"

	"p2pstream/internal/secrets"
)

const DefaultBatchSize = 500

type Field struct {
	Name            string
	Purpose         secrets.Purpose
	Table           string
	ValueColumn     string
	OwnerExpr       string
	LegacyOwnerExpr string
	ExtraWhere      string
}

var Fields = []Field{
	{
		Name:            "public route target basic auth password",
		Purpose:         secrets.PurposePublicRouteTargetBasicAuthPassword,
		Table:           "public_route_targets",
		ValueColumn:     "upstream_basic_auth_password",
		OwnerExpr:       "id",
		LegacyOwnerExpr: "NULL",
	},
	{
		Name:            "public route target sensitive upstream header",
		Purpose:         secrets.PurposePublicRouteTargetSensitiveHeader,
		Table:           "public_route_target_upstream_headers",
		ValueColumn:     "value",
		OwnerExpr:       "id",
		LegacyOwnerExpr: "target_id",
		ExtraWhere:      "AND (sensitive <> 0 OR lower(name) IN ('authorization', 'proxy-authorization', 'cookie'))",
	},
	{
		Name:            "TLS DNS credential API token",
		Purpose:         secrets.PurposePublicTLSDNSCredentialAPIToken,
		Table:           "public_tls_dns_credentials",
		ValueColumn:     "api_token",
		OwnerExpr:       "id",
		LegacyOwnerExpr: "NULL",
	},
	{
		Name:            "WAF captcha provider secret key",
		Purpose:         secrets.PurposePublicWAFCaptchaProviderSecretKey,
		Table:           "public_waf_captcha_providers",
		ValueColumn:     "secret_key",
		OwnerExpr:       "id",
		LegacyOwnerExpr: "NULL",
	},
	{
		Name:            "WAF cookie signing secret",
		Purpose:         secrets.PurposePublicWAFCookieSigningSecret,
		Table:           "public_waf_settings",
		ValueColumn:     "cookie_signing_secret",
		OwnerExpr:       "id",
		LegacyOwnerExpr: "NULL",
	},
	{
		Name:            "environment access token",
		Purpose:         secrets.PurposeEnvironmentAccessToken,
		Table:           "environments",
		ValueColumn:     "access_token",
		OwnerExpr:       "id",
		LegacyOwnerExpr: "NULL",
	},
}

type Store struct {
	DB      *sql.DB
	Secrets *secrets.Service
}

type Row struct {
	ID            int64
	OwnerID       int64
	LegacyOwnerID sql.NullInt64
	Value         string
}

type PurposeStatus struct {
	Name          string          `json:"name"`
	Purpose       secrets.Purpose `json:"purpose"`
	Total         int             `json:"total"`
	Plaintext     int             `json:"plaintext"`
	Encrypted     int             `json:"encrypted"`
	Current       int             `json:"current"`
	NeedsRewrap   int             `json:"needs_rewrap"`
	MissingKey    int             `json:"missing_key"`
	Invalid       int             `json:"invalid"`
	DecryptFailed int             `json:"decrypt_failed"`
	KeyIDs        map[string]int  `json:"key_ids,omitempty"`
	Errors        map[string]int  `json:"errors,omitempty"`
}

type Status struct {
	Purposes      []PurposeStatus `json:"purposes"`
	CurrentKeyID  string          `json:"current_key_id,omitempty"`
	Provider      string          `json:"provider,omitempty"`
	EncryptionOn  bool            `json:"encryption_enabled"`
	Required      bool            `json:"required"`
	Total         int             `json:"total"`
	Plaintext     int             `json:"plaintext"`
	Encrypted     int             `json:"encrypted"`
	Current       int             `json:"current"`
	NeedsRewrap   int             `json:"needs_rewrap"`
	MissingKey    int             `json:"missing_key"`
	Invalid       int             `json:"invalid"`
	DecryptFailed int             `json:"decrypt_failed"`
}

type ReconcileOptions struct {
	DryRun    bool
	BatchSize int
}

type PurposeReconcileResult struct {
	Name         string          `json:"name"`
	Purpose      secrets.Purpose `json:"purpose"`
	Scanned      int             `json:"scanned"`
	WouldEncrypt int             `json:"would_encrypt,omitempty"`
	WouldRewrap  int             `json:"would_rewrap,omitempty"`
	Encrypted    int             `json:"encrypted,omitempty"`
	Rewrapped    int             `json:"rewrapped,omitempty"`
	Unchanged    int             `json:"unchanged"`
}

type ReconcileResult struct {
	DryRun       bool                     `json:"dry_run"`
	Purposes     []PurposeReconcileResult `json:"purposes"`
	Scanned      int                      `json:"scanned"`
	WouldEncrypt int                      `json:"would_encrypt,omitempty"`
	WouldRewrap  int                      `json:"would_rewrap,omitempty"`
	Encrypted    int                      `json:"encrypted,omitempty"`
	Rewrapped    int                      `json:"rewrapped,omitempty"`
	Unchanged    int                      `json:"unchanged"`
}

type rowClassification struct {
	Plaintext       string
	PlaintextRow    bool
	NeedsRewrap     bool
	PlaintextRewrap bool
	MissingKey      bool
	Invalid         bool
	DecryptFail     bool
	KeyID           string
	Error           string
}

func New(database *sql.DB, service *secrets.Service) *Store {
	return &Store{DB: database, Secrets: service}
}

func (s *Store) Status(ctx context.Context, batchSize int) (Status, error) {
	if s == nil || s.DB == nil {
		return Status{}, nil
	}
	batchSize = normalizeBatchSize(batchSize)
	status := Status{
		Purposes:     make([]PurposeStatus, 0, len(Fields)),
		EncryptionOn: s.Secrets != nil && s.Secrets.Enabled(),
		Required:     s.Secrets != nil && s.Secrets.Required(),
	}
	if s.Secrets != nil {
		status.CurrentKeyID = s.Secrets.CurrentKeyID()
		status.Provider = s.Secrets.Provider()
	}
	for _, field := range Fields {
		purposeStatus := PurposeStatus{
			Name:    field.Name,
			Purpose: field.Purpose,
			KeyIDs:  make(map[string]int),
			Errors:  make(map[string]int),
		}
		if err := s.scanRows(ctx, field, batchSize, func(row Row) error {
			classification := s.classifyRow(ctx, field, row)
			addClassification(&purposeStatus, classification)
			return nil
		}); err != nil {
			return Status{}, err
		}
		if len(purposeStatus.KeyIDs) == 0 {
			purposeStatus.KeyIDs = nil
		}
		if len(purposeStatus.Errors) == 0 {
			purposeStatus.Errors = nil
		}
		addPurposeStatus(&status, purposeStatus)
		status.Purposes = append(status.Purposes, purposeStatus)
	}
	return status, nil
}

func (s *Store) Reconcile(ctx context.Context, opts ReconcileOptions) (ReconcileResult, error) {
	if s == nil || s.DB == nil {
		return ReconcileResult{DryRun: opts.DryRun}, nil
	}
	opts.BatchSize = normalizeBatchSize(opts.BatchSize)
	status, err := s.Status(ctx, opts.BatchSize)
	if err != nil {
		return ReconcileResult{DryRun: opts.DryRun}, err
	}
	if s.Secrets == nil || !s.Secrets.Enabled() {
		if status.Encrypted > 0 {
			return ReconcileResult{DryRun: opts.DryRun}, fmt.Errorf("encrypted stored secrets require a current secrets encryption key")
		}
		return dryRunResultFromStatus(opts.DryRun, status, false), nil
	}
	if status.MissingKey > 0 || status.Invalid > 0 || status.DecryptFailed > 0 {
		return ReconcileResult{DryRun: opts.DryRun}, fmt.Errorf("stored secrets cannot be safely reconciled: missing_key=%d invalid=%d decrypt_failed=%d; configure missing previous keys or inspect status before retrying", status.MissingKey, status.Invalid, status.DecryptFailed)
	}
	if s.Secrets.Required() && status.Plaintext > 0 {
		return ReconcileResult{DryRun: opts.DryRun}, fmt.Errorf("%d stored secret(s) are plaintext but secrets encryption is required", status.Plaintext)
	}
	if opts.DryRun {
		return dryRunResultFromStatus(true, status, true), nil
	}

	result := ReconcileResult{
		DryRun:   false,
		Purposes: make([]PurposeReconcileResult, 0, len(Fields)),
	}
	for _, field := range Fields {
		purposeResult, err := s.reconcileField(ctx, field, opts.BatchSize)
		if err != nil {
			return result, err
		}
		addPurposeReconcileResult(&result, purposeResult)
		result.Purposes = append(result.Purposes, purposeResult)
	}
	return result, nil
}

func (s *Store) reconcileField(ctx context.Context, field Field, batchSize int) (PurposeReconcileResult, error) {
	result := PurposeReconcileResult{Name: field.Name, Purpose: field.Purpose}
	lastID := int64(0)
	for {
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			return result, fmt.Errorf("begin %s reconcile batch: %w", field.Name, err)
		}
		rows, err := queryBatch(ctx, tx, field, lastID, batchSize)
		if err != nil {
			_ = tx.Rollback()
			return result, err
		}
		if len(rows) == 0 {
			_ = tx.Rollback()
			break
		}
		for _, row := range rows {
			lastID = row.ID
			result.Scanned++
			classification := s.classifyRow(ctx, field, row)
			if err := unsafeClassificationError(field, row, classification); err != nil {
				_ = tx.Rollback()
				return result, err
			}
			if classification.PlaintextRow {
				encrypted, err := s.Secrets.EncryptContext(ctx, field.Purpose, row.OwnerID, classification.Plaintext)
				if err != nil {
					_ = tx.Rollback()
					return result, fmt.Errorf("encrypt %s %d: %w", field.Name, row.ID, err)
				}
				if _, err := tx.ExecContext(ctx, updateSQL(field), encrypted, row.ID); err != nil {
					_ = tx.Rollback()
					return result, fmt.Errorf("update %s %d: %w", field.Name, row.ID, err)
				}
				result.Encrypted++
				continue
			}
			if classification.NeedsRewrap {
				var encrypted string
				var err error
				if classification.PlaintextRewrap {
					encrypted, err = s.Secrets.EncryptContext(ctx, field.Purpose, row.OwnerID, classification.Plaintext)
				} else {
					encrypted, err = s.Secrets.RewrapContext(ctx, field.Purpose, row.OwnerID, row.Value)
					if errors.Is(err, secrets.ErrPlaintextRewrapRequired) {
						encrypted, err = s.Secrets.EncryptContext(ctx, field.Purpose, row.OwnerID, classification.Plaintext)
					}
				}
				if err != nil {
					_ = tx.Rollback()
					return result, fmt.Errorf("rewrap %s %d: %w", field.Name, row.ID, err)
				}
				if _, err := tx.ExecContext(ctx, updateSQL(field), encrypted, row.ID); err != nil {
					_ = tx.Rollback()
					return result, fmt.Errorf("update %s %d: %w", field.Name, row.ID, err)
				}
				result.Rewrapped++
				continue
			}
			result.Unchanged++
		}
		if err := tx.Commit(); err != nil {
			return result, fmt.Errorf("commit %s reconcile batch: %w", field.Name, err)
		}
	}
	return result, nil
}

func unsafeClassificationError(field Field, row Row, classification rowClassification) error {
	switch {
	case classification.MissingKey:
		if classification.KeyID != "" {
			return fmt.Errorf("%s %d uses encrypted secret key %q that is not configured", field.Name, row.ID, classification.KeyID)
		}
		return fmt.Errorf("%s %d uses an encrypted secret key that is not configured", field.Name, row.ID)
	case classification.Invalid:
		return fmt.Errorf("%s %d has invalid encrypted secret metadata: %s", field.Name, row.ID, classification.Error)
	case classification.DecryptFail:
		return fmt.Errorf("%s %d could not be decrypted: %s", field.Name, row.ID, classification.Error)
	default:
		return nil
	}
}

func (s *Store) scanRows(ctx context.Context, field Field, batchSize int, fn func(Row) error) error {
	lastID := int64(0)
	for {
		rows, err := queryBatch(ctx, s.DB, field, lastID, batchSize)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		for _, row := range rows {
			lastID = row.ID
			if err := fn(row); err != nil {
				return err
			}
		}
	}
}

func (s *Store) classifyRow(ctx context.Context, field Field, row Row) rowClassification {
	meta, err := secrets.Inspect(row.Value)
	if err != nil {
		return rowClassification{Invalid: true, Error: err.Error()}
	}
	if meta.State == secrets.StatePlaintext {
		return rowClassification{Plaintext: row.Value, PlaintextRow: true}
	}
	classification := rowClassification{KeyID: meta.KeyID}
	if s.Secrets == nil || !s.Secrets.Enabled() || !s.Secrets.CanDecrypt(meta) {
		classification.MissingKey = true
		return classification
	}
	plaintext, _, err := s.Secrets.DecryptContext(ctx, field.Purpose, row.OwnerID, row.Value)
	if err == nil {
		needsRewrap, rewrapErr := s.Secrets.NeedsRewrapContext(ctx, row.Value)
		if rewrapErr != nil {
			classification.DecryptFail = true
			classification.Error = rewrapErr.Error()
			return classification
		}
		classification.Plaintext = plaintext
		classification.NeedsRewrap = needsRewrap
		return classification
	}
	if row.LegacyOwnerID.Valid && row.LegacyOwnerID.Int64 != row.OwnerID {
		plaintext, _, legacyErr := s.Secrets.DecryptContext(ctx, field.Purpose, row.LegacyOwnerID.Int64, row.Value)
		if legacyErr == nil {
			classification.Plaintext = plaintext
			classification.NeedsRewrap = true
			classification.PlaintextRewrap = true
			return classification
		}
	}
	classification.DecryptFail = true
	classification.Error = err.Error()
	return classification
}

func queryBatch(ctx context.Context, querier interface {
	QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error)
}, field Field, lastID int64, limit int) ([]Row, error) {
	sqlRows, err := querier.QueryContext(ctx, selectSQL(field), lastID, limit)
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", field.Name, err)
	}
	defer sqlRows.Close()
	rows := make([]Row, 0, limit)
	for sqlRows.Next() {
		var row Row
		if err := sqlRows.Scan(&row.ID, &row.OwnerID, &row.LegacyOwnerID, &row.Value); err != nil {
			return nil, fmt.Errorf("scan %s row: %w", field.Name, err)
		}
		rows = append(rows, row)
	}
	if err := sqlRows.Err(); err != nil {
		return nil, fmt.Errorf("scan %s rows: %w", field.Name, err)
	}
	return rows, nil
}

func selectSQL(field Field) string {
	return fmt.Sprintf(`SELECT id, %s, %s, %s
FROM %s
WHERE %s <> ''
  AND id > ?
  %s
ORDER BY id
LIMIT ?`, field.OwnerExpr, field.LegacyOwnerExpr, field.ValueColumn, field.Table, field.ValueColumn, field.ExtraWhere)
}

func updateSQL(field Field) string {
	return fmt.Sprintf(`UPDATE %s
SET %s = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?`, field.Table, field.ValueColumn)
}

func normalizeBatchSize(batchSize int) int {
	if batchSize <= 0 {
		return DefaultBatchSize
	}
	return batchSize
}

func addClassification(status *PurposeStatus, classification rowClassification) {
	status.Total++
	if classification.KeyID != "" {
		status.KeyIDs[classification.KeyID]++
	}
	if classification.Error != "" {
		status.Errors[classification.Error]++
	}
	switch {
	case classification.PlaintextRow:
		status.Plaintext++
	case classification.Invalid:
		status.Encrypted++
		status.Invalid++
	case classification.MissingKey:
		status.Encrypted++
		status.MissingKey++
	case classification.DecryptFail:
		status.Encrypted++
		status.DecryptFailed++
	case classification.NeedsRewrap:
		status.Encrypted++
		status.NeedsRewrap++
	default:
		status.Encrypted++
		status.Current++
	}
}

func addPurposeStatus(status *Status, purpose PurposeStatus) {
	status.Total += purpose.Total
	status.Plaintext += purpose.Plaintext
	status.Encrypted += purpose.Encrypted
	status.Current += purpose.Current
	status.NeedsRewrap += purpose.NeedsRewrap
	status.MissingKey += purpose.MissingKey
	status.Invalid += purpose.Invalid
	status.DecryptFailed += purpose.DecryptFailed
}

func dryRunResultFromStatus(dryRun bool, status Status, canEncrypt bool) ReconcileResult {
	result := ReconcileResult{
		DryRun:   dryRun,
		Purposes: make([]PurposeReconcileResult, 0, len(status.Purposes)),
	}
	for _, purpose := range status.Purposes {
		purposeResult := PurposeReconcileResult{
			Name:      purpose.Name,
			Purpose:   purpose.Purpose,
			Scanned:   purpose.Total,
			Unchanged: purpose.Current,
		}
		if canEncrypt {
			purposeResult.WouldEncrypt = purpose.Plaintext
			purposeResult.WouldRewrap = purpose.NeedsRewrap
		} else {
			purposeResult.Unchanged = purpose.Total
		}
		addPurposeReconcileResult(&result, purposeResult)
		result.Purposes = append(result.Purposes, purposeResult)
	}
	return result
}

func addPurposeReconcileResult(result *ReconcileResult, purpose PurposeReconcileResult) {
	result.Scanned += purpose.Scanned
	result.WouldEncrypt += purpose.WouldEncrypt
	result.WouldRewrap += purpose.WouldRewrap
	result.Encrypted += purpose.Encrypted
	result.Rewrapped += purpose.Rewrapped
	result.Unchanged += purpose.Unchanged
}

func SortedKeyIDs(keyIDs map[string]int) []string {
	keys := make([]string, 0, len(keyIDs))
	for key := range keyIDs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
