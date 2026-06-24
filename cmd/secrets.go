package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"

	"p2pstream/internal/config"
	"p2pstream/internal/db"
	"p2pstream/internal/secretfiles"
	"p2pstream/internal/secrets"
	"p2pstream/internal/secretstate"
	"p2pstream/internal/secretstore"
)

type secretsGenerateKeyOptions struct {
	Format string
	Stdout io.Writer
}

type secretsStatusOptions struct {
	DatabaseURL string
	Format      string
	BatchSize   int
	Stdout      io.Writer
}

type secretsRewrapOptions struct {
	DatabaseURL string
	Format      string
	BatchSize   int
	DryRun      bool
	Yes         bool
	Stdout      io.Writer
}

type secretsStatusOutput struct {
	secretstore.Status
	LastReconciliation *secretstate.Snapshot `json:"last_reconciliation,omitempty"`
}

var secretsCmd = &cobra.Command{
	Use:   "secrets",
	Short: "Inspect and reconcile encrypted stored secrets",
}

var secretsGenerateKeyCmd = &cobra.Command{
	Use:           "generate-key",
	Short:         "Generate a 32-byte secrets encryption key",
	SilenceErrors: true,
	SilenceUsage:  true,
	RunE: func(cmd *cobra.Command, args []string) error {
		format, _ := cmd.Flags().GetString("format")
		return runSecretsGenerateKey(secretsGenerateKeyOptions{
			Format: format,
			Stdout: cmd.OutOrStdout(),
		})
	},
}

var secretsStatusCmd = &cobra.Command{
	Use:           "status",
	Short:         "Inspect stored secret encryption state",
	SilenceErrors: true,
	SilenceUsage:  true,
	RunE: func(cmd *cobra.Command, args []string) error {
		databaseURL, _ := cmd.Flags().GetString("database-url")
		format, _ := cmd.Flags().GetString("format")
		batchSize, _ := cmd.Flags().GetInt("batch-size")
		return runSecretsStatus(cmd.Context(), secretsStatusOptions{
			DatabaseURL: databaseURL,
			Format:      format,
			BatchSize:   batchSize,
			Stdout:      cmd.OutOrStdout(),
		})
	},
}

var secretsRewrapCmd = &cobra.Command{
	Use:           "rewrap",
	Short:         "Encrypt plaintext secrets and rewrap old key IDs to the current key",
	SilenceErrors: true,
	SilenceUsage:  true,
	RunE: func(cmd *cobra.Command, args []string) error {
		databaseURL, _ := cmd.Flags().GetString("database-url")
		format, _ := cmd.Flags().GetString("format")
		batchSize, _ := cmd.Flags().GetInt("batch-size")
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		yes, _ := cmd.Flags().GetBool("yes")
		return runSecretsRewrap(cmd.Context(), secretsRewrapOptions{
			DatabaseURL: databaseURL,
			Format:      format,
			BatchSize:   batchSize,
			DryRun:      dryRun,
			Yes:         yes,
			Stdout:      cmd.OutOrStdout(),
		})
	},
}

func init() {
	rootCmd.AddCommand(secretsCmd)
	secretsCmd.AddCommand(secretsGenerateKeyCmd, secretsStatusCmd, secretsRewrapCmd)

	secretsGenerateKeyCmd.Flags().String("format", "env", "Output format: env or json")

	secretsStatusCmd.Flags().String("database-url", "", "Override DATABASE_URL for this operation")
	secretsStatusCmd.Flags().String("format", "table", "Output format: table or json")
	secretsStatusCmd.Flags().Int("batch-size", secretstore.DefaultBatchSize, "Rows to scan per database batch")

	secretsRewrapCmd.Flags().String("database-url", "", "Override DATABASE_URL for this operation")
	secretsRewrapCmd.Flags().String("format", "table", "Output format: table or json")
	secretsRewrapCmd.Flags().Int("batch-size", secretstore.DefaultBatchSize, "Rows to scan or update per database batch")
	secretsRewrapCmd.Flags().Bool("dry-run", false, "Report planned changes without writing")
	secretsRewrapCmd.Flags().Bool("yes", false, "Confirm writing encryption or rewrap changes")
}

func runSecretsGenerateKey(opts secretsGenerateKeyOptions) error {
	stdout := opts.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	format := normalizeSecretsFormat(opts.Format, "env")

	key, keyID, err := secrets.GenerateKey()
	if err != nil {
		return err
	}
	switch format {
	case "env":
		fmt.Fprintf(stdout, "SECRETS_ENCRYPTION_KEY=%s\n", key)
		fmt.Fprintf(stdout, "SECRETS_ENCRYPTION_KEY_ID=%s\n", keyID)
	case "json":
		return writeJSON(stdout, map[string]string{
			"key":    key,
			"key_id": keyID,
		})
	default:
		return fmt.Errorf("unsupported format %q; use env or json", opts.Format)
	}
	return nil
}

func runSecretsStatus(ctx context.Context, opts secretsStatusOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	stdout := opts.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	format := normalizeSecretsFormat(opts.Format, "table")

	cfg, database, service, err := openSecretsStore(opts.DatabaseURL)
	if err != nil {
		return err
	}
	defer database.Close()
	if err := service.Check(ctx); err != nil {
		return fmt.Errorf("check secrets encryption provider: %w", err)
	}

	status, err := secretstore.New(database.DB, service).Status(ctx, opts.BatchSize)
	if err != nil {
		return err
	}
	fileSpecs, err := secretfiles.Inventory(ctx, cfg, database.DB)
	if err != nil {
		return err
	}
	fileStatus, err := secretfiles.StatusFiles(ctx, service, fileSpecs)
	if err != nil {
		return err
	}
	addFileStatus(&status, fileStatus)
	lastReconciliation, err := secretstate.Get(ctx, database)
	if err != nil {
		return fmt.Errorf("read secret encryption state: %w", err)
	}
	output := secretsStatusOutput{
		Status:             status,
		LastReconciliation: lastReconciliation,
	}
	switch format {
	case "table":
		return writeSecretsStatusTable(stdout, output)
	case "json":
		return writeJSON(stdout, output)
	default:
		return fmt.Errorf("unsupported format %q; use table or json", opts.Format)
	}
}

func runSecretsRewrap(ctx context.Context, opts secretsRewrapOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	stdout := opts.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	format := normalizeSecretsFormat(opts.Format, "table")
	if !opts.DryRun && !opts.Yes {
		return errors.New("secrets rewrap requires --dry-run or --yes")
	}

	cfg, database, service, err := openSecretsStore(opts.DatabaseURL)
	if err != nil {
		return err
	}
	defer database.Close()
	if !opts.DryRun && (service == nil || !service.Enabled()) {
		return errors.New("secrets rewrap --yes requires a current secrets encryption key")
	}
	if err := service.Check(ctx); err != nil {
		return fmt.Errorf("check secrets encryption provider: %w", err)
	}

	result, err := secretstore.New(database.DB, service).Reconcile(ctx, secretstore.ReconcileOptions{
		DryRun:    opts.DryRun,
		BatchSize: opts.BatchSize,
	})
	if err != nil {
		return err
	}
	fileSpecs, err := secretfiles.Inventory(ctx, cfg, database.DB)
	if err != nil {
		return err
	}
	fileResult, err := secretfiles.Reconcile(ctx, service, fileSpecs, secretfiles.ReconcileOptions{DryRun: opts.DryRun})
	if err != nil {
		return err
	}
	if !opts.DryRun {
		if _, err := secretstate.Record(ctx, database, service, result, fileResult); err != nil {
			return fmt.Errorf("record secret encryption state: %w", err)
		}
	}
	addFileReconcileResult(&result, fileResult)
	switch format {
	case "table":
		return writeSecretsRewrapTable(stdout, result)
	case "json":
		return writeJSON(stdout, result)
	default:
		return fmt.Errorf("unsupported format %q; use table or json", opts.Format)
	}
}

func openSecretsStore(databaseURL string) (*config.Config, *db.DB, *secrets.Service, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to load config: %w", err)
	}
	databaseURL = strings.TrimSpace(databaseURL)
	if databaseURL != "" {
		cfg.DatabaseURL = databaseURL
	}
	service, err := secrets.NewService(secrets.KeyConfig{
		CurrentKey:     cfg.SecretsEncryptionKey,
		CurrentKeyID:   cfg.SecretsEncryptionKeyID,
		PreviousKeys:   cfg.SecretsEncryptionPrevious,
		Required:       cfg.SecretsEncryptionRequired,
		AllowPlaintext: !cfg.SecretsEncryptionRequired,
		Provider:       cfg.SecretsEncryptionProvider,
		VaultTransit: secrets.VaultTransitConfig{
			Address:   cfg.SecretsVaultAddress,
			Token:     cfg.SecretsVaultToken,
			MountPath: cfg.SecretsVaultMount,
			KeyName:   cfg.SecretsVaultKey,
			Namespace: cfg.SecretsVaultNamespace,
			Timeout:   cfg.SecretsVaultTimeout,
		},
	})
	if err != nil {
		return nil, nil, nil, err
	}

	previousLogLevel := zerolog.GlobalLevel()
	zerolog.SetGlobalLevel(zerolog.WarnLevel)
	database, err := db.Open(cfg.DatabaseURL)
	zerolog.SetGlobalLevel(previousLogLevel)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to open database: %w", err)
	}
	return cfg, database, service, nil
}

func writeSecretsStatusTable(stdout io.Writer, output secretsStatusOutput) error {
	status := output.Status
	fmt.Fprintf(stdout, "Encryption enabled:\t%t\n", status.EncryptionOn)
	if status.Provider != "" {
		fmt.Fprintf(stdout, "Provider:\t%s\n", status.Provider)
	}
	if status.CurrentKeyID != "" {
		fmt.Fprintf(stdout, "Current key ID:\t%s\n", status.CurrentKeyID)
	}
	fmt.Fprintf(stdout, "Required mode:\t%t\n\n", status.Required)

	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PURPOSE\tTOTAL\tPLAINTEXT\tCURRENT\tNEEDS_REWRAP\tMISSING_KEY\tINVALID\tDECRYPT_FAILED\tKEY_IDS")
	for _, purpose := range status.Purposes {
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%s\n",
			purpose.Purpose,
			purpose.Total,
			purpose.Plaintext,
			purpose.Current,
			purpose.NeedsRewrap,
			purpose.MissingKey,
			purpose.Invalid,
			purpose.DecryptFailed,
			formatKeyIDCounts(purpose.KeyIDs),
		)
	}
	fmt.Fprintf(tw, "TOTAL\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t\n",
		status.Total,
		status.Plaintext,
		status.Current,
		status.NeedsRewrap,
		status.MissingKey,
		status.Invalid,
		status.DecryptFailed,
	)
	if err := tw.Flush(); err != nil {
		return err
	}
	return writeSecretReconciliationStateTable(stdout, output.LastReconciliation)
}

func writeSecretReconciliationStateTable(stdout io.Writer, state *secretstate.Snapshot) error {
	fmt.Fprintln(stdout)
	if state == nil {
		fmt.Fprintln(stdout, "Last reconciliation:\tnone")
		return nil
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "Last reconciliation:\t%s\n", state.LastReconciledAt.Format(time.RFC3339))
	fmt.Fprintf(tw, "Recorded provider:\t%s\n", state.Provider)
	fmt.Fprintf(tw, "Recorded key ID:\t%s\n", state.CurrentKeyID)
	fmt.Fprintf(tw, "Recorded enabled:\t%t\n", state.EncryptionEnabled)
	fmt.Fprintf(tw, "Recorded required:\t%t\n", state.EncryptionRequired)
	fmt.Fprintf(tw, "Database counts:\tscanned=%d encrypted=%d rewrapped=%d unchanged=%d\n",
		state.DatabaseScanned,
		state.DatabaseEncrypted,
		state.DatabaseRewrapped,
		state.DatabaseUnchanged,
	)
	fmt.Fprintf(tw, "Private key file counts:\tscanned=%d encrypted=%d rewrapped=%d unchanged=%d\n",
		state.PrivateKeyFilesScanned,
		state.PrivateKeyFilesEncrypted,
		state.PrivateKeyFilesRewrapped,
		state.PrivateKeyFilesUnchanged,
	)
	return tw.Flush()
}

func writeSecretsRewrapTable(stdout io.Writer, result secretstore.ReconcileResult) error {
	if result.DryRun {
		fmt.Fprintln(stdout, "Dry run: no stored secrets were changed.")
	} else {
		fmt.Fprintln(stdout, "Rewrap complete.")
	}
	fmt.Fprintln(stdout)

	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PURPOSE\tSCANNED\tWOULD_ENCRYPT\tWOULD_REWRAP\tENCRYPTED\tREWRAPPED\tUNCHANGED")
	for _, purpose := range result.Purposes {
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t%d\t%d\n",
			purpose.Purpose,
			purpose.Scanned,
			purpose.WouldEncrypt,
			purpose.WouldRewrap,
			purpose.Encrypted,
			purpose.Rewrapped,
			purpose.Unchanged,
		)
	}
	fmt.Fprintf(tw, "TOTAL\t%d\t%d\t%d\t%d\t%d\t%d\n",
		result.Scanned,
		result.WouldEncrypt,
		result.WouldRewrap,
		result.Encrypted,
		result.Rewrapped,
		result.Unchanged,
	)
	return tw.Flush()
}

func addFileStatus(status *secretstore.Status, fileStatus secretfiles.Status) {
	if status == nil {
		return
	}
	for _, purpose := range fileStatus.Purposes {
		converted := secretstore.PurposeStatus{
			Name:          purpose.Name,
			Purpose:       purpose.Purpose,
			Total:         purpose.Total,
			Plaintext:     purpose.Plaintext,
			Encrypted:     purpose.Encrypted,
			Current:       purpose.Current,
			NeedsRewrap:   purpose.NeedsRewrap,
			MissingKey:    purpose.MissingKey,
			Invalid:       purpose.Invalid,
			DecryptFailed: purpose.DecryptFailed,
			KeyIDs:        purpose.KeyIDs,
			Errors:        purpose.Errors,
		}
		status.Purposes = append(status.Purposes, converted)
		status.Total += converted.Total
		status.Plaintext += converted.Plaintext
		status.Encrypted += converted.Encrypted
		status.Current += converted.Current
		status.NeedsRewrap += converted.NeedsRewrap
		status.MissingKey += converted.MissingKey
		status.Invalid += converted.Invalid
		status.DecryptFailed += converted.DecryptFailed
	}
}

func addFileReconcileResult(result *secretstore.ReconcileResult, fileResult secretfiles.ReconcileResult) {
	if result == nil {
		return
	}
	for _, purpose := range fileResult.Purposes {
		converted := secretstore.PurposeReconcileResult{
			Name:         purpose.Name,
			Purpose:      purpose.Purpose,
			Scanned:      purpose.Scanned,
			WouldEncrypt: purpose.WouldEncrypt,
			WouldRewrap:  purpose.WouldRewrap,
			Encrypted:    purpose.Encrypted,
			Rewrapped:    purpose.Rewrapped,
			Unchanged:    purpose.Unchanged,
		}
		result.Purposes = append(result.Purposes, converted)
		result.Scanned += converted.Scanned
		result.WouldEncrypt += converted.WouldEncrypt
		result.WouldRewrap += converted.WouldRewrap
		result.Encrypted += converted.Encrypted
		result.Rewrapped += converted.Rewrapped
		result.Unchanged += converted.Unchanged
	}
}

func writeJSON(stdout io.Writer, value interface{}) error {
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func formatKeyIDCounts(keyIDs map[string]int) string {
	if len(keyIDs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(keyIDs))
	for _, keyID := range secretstore.SortedKeyIDs(keyIDs) {
		parts = append(parts, fmt.Sprintf("%s=%d", keyID, keyIDs[keyID]))
	}
	return strings.Join(parts, ",")
}

func normalizeSecretsFormat(format, defaultFormat string) string {
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "" {
		return defaultFormat
	}
	return format
}
