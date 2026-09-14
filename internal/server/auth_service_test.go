package server

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
)

func TestLoginRejectsIncompleteThrottleConstruction(t *testing.T) {
	database := newServerTestDB(t)
	for _, shared := range []bool{false, true} {
		app := &App{DB: database, LoginThrottle: newLoginThrottle(10)}
		if shared {
			app.clientLoginThrottle = app.LoginThrottle
		}
		req := connect.NewRequest(&p2pstreamv1.LoginRequest{Username: "admin", Password: "password"})
		if _, err := app.Login(context.Background(), req); connect.CodeOf(err) != connect.CodeFailedPrecondition {
			t.Fatalf("login without independent trackers (shared=%t): %v", shared, err)
		}
	}
}
