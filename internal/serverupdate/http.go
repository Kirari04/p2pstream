package serverupdate

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

type Request struct {
	Version     string        `json:"version,omitempty"`
	OperationID string        `json:"operation_id,omitempty"`
	Start       *StartRequest `json:"start,omitempty"`
}
type Response struct {
	Error     string     `json:"error,omitempty"`
	Overview  *Overview  `json:"overview,omitempty"`
	Plan      *Plan      `json:"plan,omitempty"`
	Operation *Operation `json:"operation,omitempty"`
}

func (e *Engine) Handler(token string) http.Handler {
	// One request at a time bounds expensive Docker/release work. The worker
	// itself does not hold the HTTP admission slot during replacement.
	gate := make(chan struct{}, 1)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if len(token) < 32 || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		select {
		case gate <- struct{}{}:
			defer func() { <-gate }()
		default:
			http.Error(w, "updater busy", http.StatusTooManyRequests)
			return
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
		if err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		var request Request
		if err := decode(data, &request); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		var response Response
		switch r.URL.Path {
		case "/overview":
			var o Overview
			o, err = e.Overview(r.Context())
			response.Overview = &o
		case "/preview":
			var p Plan
			p, err = e.Preview(r.Context(), request.Version)
			response.Plan = &p
		case "/start":
			if request.Start == nil {
				err = errors.New("start request required")
			} else {
				var o Operation
				o, err = e.Start(r.Context(), *request.Start)
				response.Operation = &o
			}
		case "/operation":
			response.Operation, err = e.Operation(request.OperationID)
		default:
			http.NotFound(w, r)
			return
		}
		if err != nil {
			response = Response{Error: err.Error()}
			w.WriteHeader(http.StatusConflict)
		}
		_ = json.NewEncoder(w).Encode(response)
	})
}

func Call(ctx context.Context, socket, token, path string, request Request) (Response, error) {
	if socket == "" || !strings.HasPrefix(socket, "/") {
		return Response{}, errors.New("server updater is not installed")
	}
	data, err := json.Marshal(request)
	if err != nil {
		return Response{}, err
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 90 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://updater"+path, bytes.NewReader(data))
	if err != nil {
		return Response{}, err
	}
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(r)
	if err != nil {
		return Response{}, errors.New("server updater is unreachable")
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil {
		return Response{}, err
	}
	var result Response
	if err := decode(out, &result); err != nil {
		return Response{}, errors.New("invalid updater response")
	}
	if result.Error != "" {
		return result, errors.New(result.Error)
	}
	if resp.StatusCode != http.StatusOK {
		return result, errors.New("updater request failed")
	}
	return result, nil
}
