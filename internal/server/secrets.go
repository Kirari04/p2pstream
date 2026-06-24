package server

import (
	"context"
	"fmt"

	"github.com/rs/zerolog/log"

	"p2pstream/internal/config"
	"p2pstream/internal/secretfiles"
	"p2pstream/internal/secrets"
	"p2pstream/internal/secretstore"
)

func newSecretService(cfg *config.Config) (*secrets.Service, error) {
	if cfg == nil {
		return secrets.NewDisabledService(), nil
	}
	return secrets.NewService(secrets.KeyConfig{
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
}

func (a *App) InitializeSecretStorage(ctx context.Context) error {
	if a == nil {
		return nil
	}
	if a.secretStoreError != nil {
		return fmt.Errorf("initialize secret storage: %w", a.secretStoreError)
	}
	if a.Secrets == nil {
		a.Secrets = secrets.NewDisabledService()
	}
	if a.DB == nil {
		return nil
	}
	if err := a.Secrets.Check(ctx); err != nil {
		return fmt.Errorf("check secrets encryption provider: %w", err)
	}
	result, err := secretstore.New(a.DB.DB, a.Secrets).Reconcile(ctx, secretstore.ReconcileOptions{})
	if err != nil {
		return err
	}
	fileSpecs, err := secretfiles.Inventory(ctx, a.Config, a.DB.DB)
	if err != nil {
		return err
	}
	fileResult, err := secretfiles.Reconcile(ctx, a.Secrets, fileSpecs, secretfiles.ReconcileOptions{})
	if err != nil {
		return err
	}
	migrated := result.Encrypted + result.Rewrapped
	if migrated > 0 {
		log.Info().
			Int("secrets_encrypted", result.Encrypted).
			Int("secrets_rewrapped", result.Rewrapped).
			Msg("Reconciled stored database secrets")
	}
	fileMigrated := fileResult.Encrypted + fileResult.Rewrapped
	if fileMigrated > 0 {
		log.Info().
			Int("private_keys_encrypted", fileResult.Encrypted).
			Int("private_keys_rewrapped", fileResult.Rewrapped).
			Msg("Reconciled app-owned private key files")
	}
	log.Info().
		Bool("encryption_enabled", a.Secrets.Enabled()).
		Bool("encryption_required", a.Secrets.Required()).
		Str("provider", a.Secrets.Provider()).
		Str("current_key_id", a.Secrets.CurrentKeyID()).
		Int("database_scanned", result.Scanned).
		Int("database_encrypted", result.Encrypted).
		Int("database_rewrapped", result.Rewrapped).
		Int("database_unchanged", result.Unchanged).
		Int("private_key_files_scanned", fileResult.Scanned).
		Int("private_key_files_encrypted", fileResult.Encrypted).
		Int("private_key_files_rewrapped", fileResult.Rewrapped).
		Int("private_key_files_unchanged", fileResult.Unchanged).
		Msg("Secret storage initialized")
	return nil
}

func (a *App) encryptSecret(purpose secrets.Purpose, ownerID int64, plaintext string) (string, error) {
	if a == nil || a.Secrets == nil {
		return plaintext, nil
	}
	return a.Secrets.Encrypt(purpose, ownerID, plaintext)
}

func (a *App) decryptSecret(purpose secrets.Purpose, ownerID int64, stored string) (string, secrets.State, error) {
	if a == nil || a.Secrets == nil {
		return stored, secrets.StatePlaintext, nil
	}
	return a.Secrets.Decrypt(purpose, ownerID, stored)
}
