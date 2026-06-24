package secretfiles

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"p2pstream/internal/config"
	"p2pstream/internal/secrets"
)

const (
	ManagementCAKeyOwnerID     int64 = 1
	ManagementServerKeyOwnerID int64 = 2
)

type FileSpec struct {
	Name    string
	Purpose secrets.Purpose
	OwnerID int64
	Path    string
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
	DryRun bool
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

type classification struct {
	Plaintext     string
	PlaintextFile bool
	NeedsRewrap   bool
	MissingKey    bool
	Invalid       bool
	DecryptFailed bool
	KeyID         string
	Error         string
}

func ManagementSpecs(cfg *config.Config) []FileSpec {
	managementDir := filepath.Join(certsDir(cfg), "management")
	return []FileSpec{
		{
			Name:    "management CA private key",
			Purpose: secrets.PurposeFileManagementTLSCAKey,
			OwnerID: ManagementCAKeyOwnerID,
			Path:    filepath.Join(managementDir, "ca.key.pem"),
		},
		{
			Name:    "management server private key",
			Purpose: secrets.PurposeFileManagementTLSServerKey,
			OwnerID: ManagementServerKeyOwnerID,
			Path:    filepath.Join(managementDir, "server.key.pem"),
		},
	}
}

func PublicTLSKeySpec(name string, certID int64, keyPath string) FileSpec {
	return FileSpec{
		Name:    name,
		Purpose: secrets.PurposeFilePublicTLSPrivateKey,
		OwnerID: certID,
		Path:    keyPath,
	}
}

func ACMEAccountKeySpec(ca, accountName, keyPath string) FileSpec {
	return FileSpec{
		Name:    "ACME account private key",
		Purpose: secrets.PurposeFileACMEAccountKey,
		OwnerID: ACMEAccountOwnerID(ca, accountName),
		Path:    keyPath,
	}
}

func ACMEAccountOwnerID(ca, accountName string) int64 {
	sum := sha256.Sum256([]byte(strings.TrimSpace(ca) + "\x00" + strings.TrimSpace(accountName)))
	return int64(binary.BigEndian.Uint64(sum[:8]) & uint64(^uint64(0)>>1))
}

func Inventory(ctx context.Context, cfg *config.Config, database *sql.DB) ([]FileSpec, error) {
	specs := ManagementSpecs(cfg)
	if database != nil {
		publicSpecs, err := publicTLSKeySpecs(ctx, cfg, database)
		if err != nil {
			return nil, err
		}
		specs = append(specs, publicSpecs...)
	}
	acmeSpecs, err := acmeAccountKeySpecs(cfg)
	if err != nil {
		return nil, err
	}
	specs = append(specs, acmeSpecs...)
	return uniqueSpecs(specs), nil
}

func ReadPrivateKey(ctx context.Context, service *secrets.Service, purpose secrets.Purpose, ownerID int64, path string) ([]byte, secrets.State, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, secrets.StatePlaintext, err
	}
	stored := string(raw)
	if secrets.IsEncrypted(stored) {
		plaintext, state, err := service.DecryptContext(ctx, purpose, ownerID, stored)
		if err != nil {
			return nil, state, err
		}
		return []byte(plaintext), state, nil
	}
	if service == nil || !service.Enabled() {
		if service != nil && service.Required() {
			return nil, secrets.StatePlaintext, fmt.Errorf("plaintext private key %q is not allowed when secrets encryption is required", path)
		}
		return raw, secrets.StatePlaintext, nil
	}
	encrypted, err := service.EncryptContext(ctx, purpose, ownerID, stored)
	if err != nil {
		return nil, secrets.StatePlaintext, err
	}
	if err := writeFileAtomic(path, []byte(encrypted), 0600); err != nil {
		return nil, secrets.StatePlaintext, err
	}
	return raw, secrets.StatePlaintext, nil
}

func WritePrivateKey(ctx context.Context, service *secrets.Service, purpose secrets.Purpose, ownerID int64, path string, pemBytes []byte) error {
	if len(pemBytes) == 0 {
		return writeFileAtomic(path, nil, 0600)
	}
	out := string(pemBytes)
	if service != nil && service.Enabled() {
		encrypted, err := service.EncryptContext(ctx, purpose, ownerID, out)
		if err != nil {
			return err
		}
		out = encrypted
	} else if service != nil && service.Required() {
		return errors.New("secrets encryption is required but no current provider is configured")
	}
	return writeFileAtomic(path, []byte(out), 0600)
}

func StatusFiles(ctx context.Context, service *secrets.Service, specs []FileSpec) (Status, error) {
	status := Status{
		EncryptionOn: service != nil && service.Enabled(),
		Required:     service != nil && service.Required(),
	}
	if service != nil {
		status.CurrentKeyID = service.CurrentKeyID()
		status.Provider = service.Provider()
	}
	for _, spec := range groupSpecs(specs) {
		purposeStatus := PurposeStatus{
			Name:    spec.Name,
			Purpose: spec.Purpose,
			KeyIDs:  make(map[string]int),
			Errors:  make(map[string]int),
		}
		for _, file := range spec.Files {
			class, ok, err := classifyFile(ctx, service, file)
			if err != nil {
				return Status{}, err
			}
			if !ok {
				continue
			}
			addClassification(&purposeStatus, class)
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

func Reconcile(ctx context.Context, service *secrets.Service, specs []FileSpec, opts ReconcileOptions) (ReconcileResult, error) {
	status, err := StatusFiles(ctx, service, specs)
	if err != nil {
		return ReconcileResult{DryRun: opts.DryRun}, err
	}
	if service == nil || !service.Enabled() {
		if status.Encrypted > 0 {
			return ReconcileResult{DryRun: opts.DryRun}, errors.New("encrypted private key files require a current secrets encryption key")
		}
		return dryRunResultFromStatus(opts.DryRun, status, false), nil
	}
	if status.MissingKey > 0 || status.Invalid > 0 || status.DecryptFailed > 0 {
		return ReconcileResult{DryRun: opts.DryRun}, fmt.Errorf("private key files cannot be safely reconciled: missing_key=%d invalid=%d decrypt_failed=%d", status.MissingKey, status.Invalid, status.DecryptFailed)
	}
	if opts.DryRun {
		return dryRunResultFromStatus(true, status, true), nil
	}

	result := ReconcileResult{
		DryRun:   false,
		Purposes: make([]PurposeReconcileResult, 0, len(status.Purposes)),
	}
	for _, group := range groupSpecs(specs) {
		purposeResult := PurposeReconcileResult{Name: group.Name, Purpose: group.Purpose}
		for _, spec := range group.Files {
			class, ok, err := classifyFile(ctx, service, spec)
			if err != nil {
				return result, err
			}
			if !ok {
				continue
			}
			purposeResult.Scanned++
			switch {
			case class.MissingKey:
				return result, fmt.Errorf("%s uses an encrypted private key that is not configured", spec.Name)
			case class.Invalid:
				return result, fmt.Errorf("%s has invalid encrypted private key metadata: %s", spec.Name, class.Error)
			case class.DecryptFailed:
				return result, fmt.Errorf("%s could not be decrypted: %s", spec.Name, class.Error)
			case class.PlaintextFile:
				if err := WritePrivateKey(ctx, service, spec.Purpose, spec.OwnerID, spec.Path, []byte(class.Plaintext)); err != nil {
					return result, fmt.Errorf("encrypt %s: %w", spec.Name, err)
				}
				purposeResult.Encrypted++
			case class.NeedsRewrap:
				if err := rewrapFile(ctx, service, spec); err != nil {
					return result, fmt.Errorf("rewrap %s: %w", spec.Name, err)
				}
				purposeResult.Rewrapped++
			default:
				purposeResult.Unchanged++
			}
		}
		addPurposeReconcileResult(&result, purposeResult)
		result.Purposes = append(result.Purposes, purposeResult)
	}
	return result, nil
}

func publicTLSKeySpecs(ctx context.Context, cfg *config.Config, database *sql.DB) ([]FileSpec, error) {
	rows, err := database.QueryContext(ctx, `SELECT id, listener_id, key_path FROM public_tls_certificates WHERE key_path <> '' ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("scan public TLS private key files: %w", err)
	}
	defer rows.Close()

	var specs []FileSpec
	for rows.Next() {
		var id, listenerID int64
		var keyPath string
		if err := rows.Scan(&id, &listenerID, &keyPath); err != nil {
			return nil, fmt.Errorf("scan public TLS private key row: %w", err)
		}
		if !IsManagedPublicTLSKeyPath(cfg, listenerID, id, keyPath) {
			continue
		}
		specs = append(specs, PublicTLSKeySpec("public TLS private key", id, keyPath))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan public TLS private key rows: %w", err)
	}
	return specs, nil
}

func IsManagedPublicTLSKeyPath(cfg *config.Config, listenerID, certID int64, keyPath string) bool {
	keyPath = strings.TrimSpace(keyPath)
	if keyPath == "" {
		return false
	}
	if cfg == nil {
		cfg = &config.Config{}
	}
	_, expectedKeyPath := cfg.PublicTLSCertificatePaths(listenerID, certID)
	return sameCleanPath(keyPath, expectedKeyPath)
}

func acmeAccountKeySpecs(cfg *config.Config) ([]FileSpec, error) {
	pattern := filepath.Join(certsDir(cfg), "acme", "accounts", "*", "*.key")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("scan ACME account private key files: %w", err)
	}
	specs := make([]FileSpec, 0, len(matches))
	for _, path := range matches {
		ca := filepath.Base(filepath.Dir(path))
		account := strings.TrimSuffix(filepath.Base(path), ".key")
		specs = append(specs, ACMEAccountKeySpec(ca, account, path))
	}
	return specs, nil
}

func classifyFile(ctx context.Context, service *secrets.Service, spec FileSpec) (classification, bool, error) {
	raw, err := os.ReadFile(spec.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return classification{}, false, nil
		}
		return classification{}, false, err
	}
	stored := string(raw)
	meta, err := secrets.Inspect(stored)
	if err != nil {
		return classification{Invalid: true, Error: err.Error()}, true, nil
	}
	if meta.State == secrets.StatePlaintext {
		return classification{Plaintext: stored, PlaintextFile: true}, true, nil
	}
	class := classification{KeyID: meta.KeyID}
	if service == nil || !service.Enabled() || !service.CanDecrypt(meta) {
		class.MissingKey = true
		return class, true, nil
	}
	plaintext, _, err := service.DecryptContext(ctx, spec.Purpose, spec.OwnerID, stored)
	if err != nil {
		class.DecryptFailed = true
		class.Error = err.Error()
		return class, true, nil
	}
	needsRewrap, err := service.NeedsRewrapContext(ctx, stored)
	if err != nil {
		class.DecryptFailed = true
		class.Error = err.Error()
		return class, true, nil
	}
	class.Plaintext = plaintext
	class.NeedsRewrap = needsRewrap
	return class, true, nil
}

func rewrapFile(ctx context.Context, service *secrets.Service, spec FileSpec) error {
	storedBytes, err := os.ReadFile(spec.Path)
	if err != nil {
		return err
	}
	stored := string(storedBytes)
	rewrapped, err := service.RewrapContext(ctx, spec.Purpose, spec.OwnerID, stored)
	if errors.Is(err, secrets.ErrPlaintextRewrapRequired) {
		plaintext, _, decryptErr := service.DecryptContext(ctx, spec.Purpose, spec.OwnerID, stored)
		if decryptErr != nil {
			return decryptErr
		}
		rewrapped, err = service.EncryptContext(ctx, spec.Purpose, spec.OwnerID, plaintext)
	}
	if err != nil {
		return err
	}
	return writeFileAtomic(spec.Path, []byte(rewrapped), 0600)
}

func writeFileAtomic(path string, contents []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create directory %q: %w", dir, err)
	}
	if dir != "." && dir != string(filepath.Separator) {
		if err := os.Chmod(dir, 0700); err != nil {
			return fmt.Errorf("set directory permissions %q: %w", dir, err)
		}
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary file for %q: %w", path, err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set temporary file permissions for %q: %w", path, err)
	}
	if _, err := tmp.Write(contents); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temporary file for %q: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temporary file for %q: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary file for %q: %w", path, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace %q: %w", path, err)
	}
	cleanup = false
	if err := os.Chmod(path, mode); err != nil {
		return fmt.Errorf("set file permissions %q: %w", path, err)
	}
	_ = syncDir(dir)
	return nil
}

func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

type specGroup struct {
	Name    string
	Purpose secrets.Purpose
	Files   []FileSpec
}

func groupSpecs(specs []FileSpec) []specGroup {
	groupsByPurpose := make(map[secrets.Purpose]*specGroup)
	var order []secrets.Purpose
	for _, spec := range uniqueSpecs(specs) {
		if strings.TrimSpace(spec.Path) == "" {
			continue
		}
		group := groupsByPurpose[spec.Purpose]
		if group == nil {
			group = &specGroup{Name: spec.Name, Purpose: spec.Purpose}
			groupsByPurpose[spec.Purpose] = group
			order = append(order, spec.Purpose)
		}
		group.Files = append(group.Files, spec)
	}
	groups := make([]specGroup, 0, len(order))
	for _, purpose := range order {
		group := groupsByPurpose[purpose]
		sort.SliceStable(group.Files, func(i, j int) bool {
			if group.Files[i].OwnerID == group.Files[j].OwnerID {
				return group.Files[i].Path < group.Files[j].Path
			}
			return group.Files[i].OwnerID < group.Files[j].OwnerID
		})
		groups = append(groups, *group)
	}
	return groups
}

func uniqueSpecs(specs []FileSpec) []FileSpec {
	seen := make(map[string]struct{})
	out := make([]FileSpec, 0, len(specs))
	for _, spec := range specs {
		key := fmt.Sprintf("%s\x00%d\x00%s", spec.Purpose, spec.OwnerID, filepath.Clean(spec.Path))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, spec)
	}
	return out
}

func addClassification(status *PurposeStatus, class classification) {
	status.Total++
	if class.KeyID != "" {
		status.KeyIDs[class.KeyID]++
	}
	if class.Error != "" {
		status.Errors[class.Error]++
	}
	switch {
	case class.PlaintextFile:
		status.Plaintext++
	case class.Invalid:
		status.Encrypted++
		status.Invalid++
	case class.MissingKey:
		status.Encrypted++
		status.MissingKey++
	case class.DecryptFailed:
		status.Encrypted++
		status.DecryptFailed++
	case class.NeedsRewrap:
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

func certsDir(cfg *config.Config) string {
	if cfg != nil {
		if strings.TrimSpace(cfg.CertsDir) != "" {
			return cfg.CertsDir
		}
		if strings.TrimSpace(cfg.ConfigDir) != "" {
			return filepath.Join(filepath.Clean(cfg.ConfigDir), "certs")
		}
	}
	return filepath.Join(config.DefaultConfigDir, "certs")
}

func sameCleanPath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA == nil && errB == nil {
		return filepath.Clean(absA) == filepath.Clean(absB)
	}
	return filepath.Clean(a) == filepath.Clean(b)
}
