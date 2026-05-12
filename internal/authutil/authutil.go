package authutil

import (
	"errors"
	"regexp"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

const (
	MinimumPasswordLength = 12
	passwordHashCost      = 12
)

var usernamePattern = regexp.MustCompile(`^[a-z0-9_-]{3,64}$`)

func NormalizeUsername(username string) string {
	return strings.TrimSpace(strings.ToLower(username))
}

func ValidateUsername(username string) error {
	if !usernamePattern.MatchString(username) {
		return errors.New("username must be 3-64 lowercase letters, numbers, underscores, or hyphens")
	}
	return nil
}

func ValidatePassword(password string) error {
	if len(password) < MinimumPasswordLength {
		return errors.New("password must be at least 12 characters")
	}
	return nil
}

func HashPassword(password string) (string, error) {
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), passwordHashCost)
	if err != nil {
		return "", err
	}
	return string(passwordHash), nil
}

func ComparePasswordHash(hash string, password string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
}
