package cmd

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"p2pstream/internal/authutil"
	"p2pstream/internal/config"
	"p2pstream/internal/db"
)

type resetPasswordOptions struct {
	Username     string
	DatabaseURL  string
	PasswordEnv  string
	PasswordFile string
	Stdin        *os.File
	Stdout       io.Writer
	Stderr       io.Writer
}

var usersCmd = &cobra.Command{
	Use:   "users",
	Short: "Manage p2pstream management users",
}

var usersResetPasswordCmd = &cobra.Command{
	Use:           "reset-password USERNAME",
	Short:         "Reset a management user password in the local database",
	Args:          cobra.ExactArgs(1),
	SilenceErrors: true,
	SilenceUsage:  true,
	RunE: func(cmd *cobra.Command, args []string) error {
		databaseURL, _ := cmd.Flags().GetString("database-url")
		passwordEnv, _ := cmd.Flags().GetString("password-env")
		passwordFile, _ := cmd.Flags().GetString("password-file")
		return runResetPassword(cmd.Context(), resetPasswordOptions{
			Username:     args[0],
			DatabaseURL:  databaseURL,
			PasswordEnv:  passwordEnv,
			PasswordFile: passwordFile,
			Stdin:        os.Stdin,
			Stdout:       cmd.OutOrStdout(),
			Stderr:       cmd.ErrOrStderr(),
		})
	},
}

func init() {
	rootCmd.AddCommand(usersCmd)
	usersCmd.AddCommand(usersResetPasswordCmd)
	usersResetPasswordCmd.Flags().String("database-url", "", "Override DATABASE_URL for this operation")
	usersResetPasswordCmd.Flags().String("password-env", "", "Read the new password from the named environment variable")
	usersResetPasswordCmd.Flags().String("password-file", "", "Read the new password from a file")
}

func runResetPassword(ctx context.Context, opts resetPasswordOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	stdout := opts.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	stderr := opts.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}

	username := authutil.NormalizeUsername(opts.Username)
	if err := authutil.ValidateUsername(username); err != nil {
		return err
	}

	password, err := resetPasswordValue(opts, stderr)
	if err != nil {
		return err
	}
	if err := authutil.ValidatePassword(password); err != nil {
		return err
	}

	databaseURL := strings.TrimSpace(opts.DatabaseURL)
	if databaseURL == "" {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
		databaseURL = cfg.DatabaseURL
	}

	previousLogLevel := zerolog.GlobalLevel()
	zerolog.SetGlobalLevel(zerolog.WarnLevel)
	database, err := db.Open(databaseURL)
	zerolog.SetGlobalLevel(previousLogLevel)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer database.Close()

	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to start password reset transaction: %w", err)
	}
	defer tx.Rollback()

	qtx := database.WithTx(tx)
	user, err := qtx.GetUserByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("active user %q not found", username)
		}
		return fmt.Errorf("failed to load user %q: %w", username, err)
	}

	passwordHash, err := authutil.HashPassword(password)
	if err != nil {
		return fmt.Errorf("failed to hash password: %w", err)
	}
	if _, err := qtx.UpdateUserPassword(ctx, db.UpdateUserPasswordParams{
		PasswordHash: passwordHash,
		ID:           user.ID,
	}); err != nil {
		return fmt.Errorf("failed to update password for user %q: %w", username, err)
	}
	revokedSessions, err := qtx.RevokeUserSessions(ctx, user.ID)
	if err != nil {
		return fmt.Errorf("failed to revoke sessions for user %q: %w", username, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit password reset: %w", err)
	}

	fmt.Fprintf(stdout, "Password reset for user %q; revoked %d active session(s).\n", username, revokedSessions)
	return nil
}

func resetPasswordValue(opts resetPasswordOptions, stderr io.Writer) (string, error) {
	passwordEnv := strings.TrimSpace(opts.PasswordEnv)
	passwordFile := strings.TrimSpace(opts.PasswordFile)
	sources := 0
	if passwordEnv != "" {
		sources++
	}
	if passwordFile != "" {
		sources++
	}
	if sources > 1 {
		return "", errors.New("use only one password source: prompt, --password-env, or --password-file")
	}
	if passwordEnv != "" {
		password, ok := os.LookupEnv(passwordEnv)
		if !ok || password == "" {
			return "", fmt.Errorf("environment variable %s is not set or is empty", passwordEnv)
		}
		return password, nil
	}
	if passwordFile != "" {
		passwordBytes, err := os.ReadFile(passwordFile)
		if err != nil {
			return "", fmt.Errorf("failed to read password file %q: %w", passwordFile, err)
		}
		password := string(passwordBytes)
		if strings.HasSuffix(password, "\r\n") {
			password = strings.TrimSuffix(password, "\r\n")
		} else {
			password = strings.TrimSuffix(password, "\n")
		}
		if password == "" {
			return "", fmt.Errorf("password file %q is empty", passwordFile)
		}
		return password, nil
	}
	return promptResetPassword(opts.Stdin, stderr)
}

func promptResetPassword(stdin *os.File, stderr io.Writer) (string, error) {
	if stdin == nil {
		stdin = os.Stdin
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	fd := int(stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", errors.New("password prompt requires a terminal; use --password-env or --password-file for noninteractive use")
	}
	fmt.Fprint(stderr, "New password: ")
	passwordBytes, err := term.ReadPassword(fd)
	fmt.Fprintln(stderr)
	if err != nil {
		return "", fmt.Errorf("failed to read password: %w", err)
	}
	fmt.Fprint(stderr, "Confirm new password: ")
	confirmBytes, err := term.ReadPassword(fd)
	fmt.Fprintln(stderr)
	if err != nil {
		return "", fmt.Errorf("failed to read password confirmation: %w", err)
	}
	password := string(passwordBytes)
	if password != string(confirmBytes) {
		return "", errors.New("passwords do not match")
	}
	return password, nil
}
