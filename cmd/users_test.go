package cmd

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"p2pstream/internal/authutil"
	"p2pstream/internal/db"
)

const (
	resetOldPassword = "old correct horse battery staple"
	resetNewPassword = "new correct horse battery staple"
)

func TestResetPasswordFromEnvRevokesSessions(t *testing.T) {
	dbPath, tokenHash := seedResetPasswordUser(t, "admin")
	t.Setenv("RESET_PASSWORD", resetNewPassword)

	var out bytes.Buffer
	if err := runResetPassword(context.Background(), resetPasswordOptions{
		Username:    "admin",
		DatabaseURL: dbPath,
		PasswordEnv: "RESET_PASSWORD",
		Stdout:      &out,
		Stderr:      io.Discard,
	}); err != nil {
		t.Fatalf("reset password: %v", err)
	}

	database := openResetPasswordTestDB(t, dbPath)
	user, err := database.GetUserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatalf("get user after reset: %v", err)
	}
	if err := authutil.ComparePasswordHash(user.PasswordHash, resetNewPassword); err != nil {
		t.Fatalf("new password does not match stored hash: %v", err)
	}
	if err := authutil.ComparePasswordHash(user.PasswordHash, resetOldPassword); err == nil {
		t.Fatal("old password still matches stored hash")
	}
	if !sessionRevoked(t, database, tokenHash) {
		t.Fatal("expected reset to revoke existing session")
	}
	output := out.String()
	if !strings.Contains(output, `Password reset for user "admin"; revoked 1 active session(s).`) {
		t.Fatalf("unexpected output: %q", output)
	}
	if strings.Contains(output, resetNewPassword) || strings.Contains(output, user.PasswordHash) {
		t.Fatalf("output leaked password material: %q", output)
	}
}

func TestResetPasswordFromFileTrimsOneTrailingNewline(t *testing.T) {
	dbPath, _ := seedResetPasswordUser(t, "admin")
	passwordFile := filepath.Join(t.TempDir(), "password.txt")
	if err := os.WriteFile(passwordFile, []byte(resetNewPassword+"\n"), 0600); err != nil {
		t.Fatalf("write password file: %v", err)
	}

	if err := runResetPassword(context.Background(), resetPasswordOptions{
		Username:     "admin",
		DatabaseURL:  dbPath,
		PasswordFile: passwordFile,
		Stdout:       io.Discard,
		Stderr:       io.Discard,
	}); err != nil {
		t.Fatalf("reset password: %v", err)
	}

	database := openResetPasswordTestDB(t, dbPath)
	user, err := database.GetUserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatalf("get user after reset: %v", err)
	}
	if err := authutil.ComparePasswordHash(user.PasswordHash, resetNewPassword); err != nil {
		t.Fatalf("new password does not match stored hash: %v", err)
	}
}

func TestResetPasswordNormalizesUsername(t *testing.T) {
	dbPath, _ := seedResetPasswordUser(t, "admin")
	t.Setenv("RESET_PASSWORD", resetNewPassword)

	if err := runResetPassword(context.Background(), resetPasswordOptions{
		Username:    "ADMIN",
		DatabaseURL: dbPath,
		PasswordEnv: "RESET_PASSWORD",
		Stdout:      io.Discard,
		Stderr:      io.Discard,
	}); err != nil {
		t.Fatalf("reset password: %v", err)
	}

	database := openResetPasswordTestDB(t, dbPath)
	user, err := database.GetUserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatalf("get normalized user after reset: %v", err)
	}
	if err := authutil.ComparePasswordHash(user.PasswordHash, resetNewPassword); err != nil {
		t.Fatalf("new password does not match stored hash: %v", err)
	}
}

func TestResetPasswordRejectsUnknownUser(t *testing.T) {
	dbPath := emptyResetPasswordDB(t)
	t.Setenv("RESET_PASSWORD", resetNewPassword)

	err := runResetPassword(context.Background(), resetPasswordOptions{
		Username:    "missing",
		DatabaseURL: dbPath,
		PasswordEnv: "RESET_PASSWORD",
		Stdout:      io.Discard,
		Stderr:      io.Discard,
	})
	if err == nil || !strings.Contains(err.Error(), `active user "missing" not found`) {
		t.Fatalf("expected unknown user error, got %v", err)
	}
}

func TestResetPasswordRejectsShortPassword(t *testing.T) {
	t.Setenv("RESET_PASSWORD", "too-short")

	err := runResetPassword(context.Background(), resetPasswordOptions{
		Username:    "admin",
		PasswordEnv: "RESET_PASSWORD",
		Stdout:      io.Discard,
		Stderr:      io.Discard,
	})
	if err == nil || !strings.Contains(err.Error(), "password must be at least 12 characters") {
		t.Fatalf("expected short password error, got %v", err)
	}
}

func TestResetPasswordRejectsMultiplePasswordSources(t *testing.T) {
	t.Setenv("RESET_PASSWORD", resetNewPassword)

	err := runResetPassword(context.Background(), resetPasswordOptions{
		Username:     "admin",
		PasswordEnv:  "RESET_PASSWORD",
		PasswordFile: "password.txt",
		Stdout:       io.Discard,
		Stderr:       io.Discard,
	})
	if err == nil || !strings.Contains(err.Error(), "use only one password source") {
		t.Fatalf("expected multiple source error, got %v", err)
	}
}

func TestResetPasswordRejectsEmptyEnvPassword(t *testing.T) {
	t.Setenv("RESET_PASSWORD", "")

	err := runResetPassword(context.Background(), resetPasswordOptions{
		Username:    "admin",
		PasswordEnv: "RESET_PASSWORD",
		Stdout:      io.Discard,
		Stderr:      io.Discard,
	})
	if err == nil || !strings.Contains(err.Error(), "environment variable RESET_PASSWORD is not set or is empty") {
		t.Fatalf("expected empty env password error, got %v", err)
	}
}

func seedResetPasswordUser(t *testing.T, username string) (dbPath string, tokenHash string) {
	t.Helper()

	dbPath = emptyResetPasswordDB(t)
	database := openResetPasswordTestDB(t, dbPath)

	passwordHash, err := authutil.HashPassword(resetOldPassword)
	if err != nil {
		t.Fatalf("hash old password: %v", err)
	}
	user, err := database.CreateUser(context.Background(), db.CreateUserParams{
		Username:     username,
		PasswordHash: passwordHash,
		Role:         "admin",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	tokenHash = "active-session-token-hash"
	if _, err := database.CreateSession(context.Background(), db.CreateSessionParams{
		UserID:    user.ID,
		TokenHash: tokenHash,
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	return dbPath, tokenHash
}

func emptyResetPasswordDB(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "p2pstream-test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open empty db: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close empty db: %v", err)
	}
	return dbPath
}

func openResetPasswordTestDB(t *testing.T, dbPath string) *db.DB {
	t.Helper()
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() {
		_ = database.Close()
	})
	return database
}

func sessionRevoked(t *testing.T, database *db.DB, tokenHash string) bool {
	t.Helper()

	var revokedAt sql.NullTime
	err := database.QueryRowContext(context.Background(), `SELECT revoked_at FROM sessions WHERE token_hash = ?`, tokenHash).Scan(&revokedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("session %q not found", tokenHash)
		}
		t.Fatalf("query session revoked_at: %v", err)
	}
	return revokedAt.Valid
}
