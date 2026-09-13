package serverupdate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPrivateAPIRejectsUnauthenticatedOversizedAndArbitraryHostInputs(t *testing.T) {
	e, d, _, request := fixtureEngine(t)
	token := strings.Repeat("a", 64)
	handler := e.Handler(token)
	for _, test := range []struct {
		path, body, auth string
		code             int
	}{
		{"/overview", "{}", "", 401},
		{"/overview", "{}", "Bearer wrong", 401},
		{"/preview", `{"version":"v1.0.1","command":"rm -rf /"}`, "Bearer " + token, 400},
		{"/start", strings.Repeat("x", 65537), "Bearer " + token, 400},
		{"/recover", "{}", "Bearer " + token, 404},
	} {
		r := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
		r.Header.Set("Authorization", test.auth)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != test.code {
			t.Fatalf("%s: %d %s", test.path, w.Code, w.Body.String())
		}
	}
	body, _ := json.Marshal(Request{Start: &request})
	// Closing a successful HTTP response cannot cancel the durable operation.
	for range 2 {
		r := httptest.NewRequest(http.MethodPost, "/start", strings.NewReader(string(body)))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
	}
	if len(d.calls) != 0 {
		t.Fatal("request lifetime performed host changes")
	}
	if err := e.execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	op, _ := e.Operation(request.OperationID)
	if op.Phase != "succeeded" {
		t.Fatal(op.Phase)
	}
}
