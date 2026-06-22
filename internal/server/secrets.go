package server

import (
	"context"
	"fmt"

	"github.com/rs/zerolog/log"

	"p2pstream/internal/config"
	"p2pstream/internal/secrets"
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
	migrated, err := a.migrateDatabaseSecrets(ctx)
	if err != nil {
		return err
	}
	if migrated > 0 {
		log.Info().Int("secrets_migrated", migrated).Msg("Encrypted stored database secrets")
	}
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
