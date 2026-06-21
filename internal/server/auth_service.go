package server

import "p2pstream/internal/db"

type authService struct {
	app      *App
	db       *db.DB
	throttle *loginThrottle
}

func newAuthService(app *App, database *db.DB, throttle *loginThrottle) *authService {
	return &authService{
		app:      app,
		db:       database,
		throttle: throttle,
	}
}

func (a *App) authService() *authService {
	if a == nil {
		return newAuthService(nil, nil, nil)
	}
	if a.auth != nil {
		return a.auth
	}
	// Directly constructed test Apps may not go through appServices; do not cache
	// here because callers can race on manually shared App values.
	return newAuthService(a, a.DB, a.LoginThrottle)
}
