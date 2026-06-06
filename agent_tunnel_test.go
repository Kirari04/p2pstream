package main_test

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/hashicorp/yamux"

	"p2pstream/internal/tunnel"
)

func runAgent(ctx context.Context, managementURL string, agentID string, token string) error {
	session, _, err := dialAgentTunnel(ctx, managementURL, agentID, token, nil)
	if err != nil {
		return err
	}
	defer session.Close()
	go func() {
		<-ctx.Done()
		_ = session.Close()
	}()
	return serveTestAgentTunnel(ctx, session)
}

func dialAgentTunnel(ctx context.Context, managementURL string, publicID string, token string, client *http.Client) (*yamux.Session, *http.Response, error) {
	tunnelURL, err := testTunnelURL(managementURL)
	if err != nil {
		return nil, nil, err
	}
	httpClient := testTunnelHTTPClient(client)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tunnelURL, nil)
	if err != nil {
		return nil, nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("X-P2PStream-Agent-ID", publicID)
	req.Header.Set("X-P2PStream-Agent-Name", publicID)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", tunnel.UpgradeToken)
	req.Header.Set(tunnel.TunnelVersionHeader, fmt.Sprintf("%d", tunnel.ProtocolVersion))

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, resp, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		return nil, resp, fmt.Errorf("expected tunnel upgrade status 101, got %d", resp.StatusCode)
	}
	body, ok := resp.Body.(io.ReadWriteCloser)
	if !ok {
		_ = resp.Body.Close()
		return nil, resp, fmt.Errorf("upgrade response body is %T, want io.ReadWriteCloser", resp.Body)
	}
	session, err := yamux.Client(body, tunnel.DefaultYamuxConfig(nil))
	if err != nil {
		_ = body.Close()
		return nil, resp, err
	}
	return session, resp, nil
}

func testTunnelURL(managementURL string) (string, error) {
	parsed, err := url.Parse(managementURL)
	if err != nil {
		return "", err
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + tunnel.BootstrapPath
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func testTunnelHTTPClient(client *http.Client) *http.Client {
	if client == nil {
		client = http.DefaultClient
	}
	base, ok := client.Transport.(*http.Transport)
	if !ok {
		if client.Transport != nil {
			return client
		}
		base = http.DefaultTransport.(*http.Transport)
	}
	transport := base.Clone()
	transport.ForceAttemptHTTP2 = false
	transport.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	transport.Protocols = protocols
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	} else {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	}
	transport.TLSClientConfig.NextProtos = []string{"http/1.1"}
	return &http.Client{Transport: transport}
}

func serveTestAgentTunnel(ctx context.Context, session *yamux.Session) error {
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		stream, err := session.Accept()
		if err != nil {
			if ctx.Err() != nil || session.IsClosed() {
				return nil
			}
			return err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			handleTestAgentStream(stream)
		}()
	}
}

func handleTestAgentStream(stream net.Conn) {
	defer stream.Close()
	open, err := tunnel.ReadOpenRequest(stream)
	if err != nil {
		return
	}
	upstream, err := (&net.Dialer{}).DialContext(context.Background(), open.Network, open.Address)
	if err != nil {
		_ = tunnel.WriteOpenResponse(stream, tunnel.OpenResponse{OK: false, ErrorKind: "dial_failed", Error: err.Error()})
		return
	}
	if err := tunnel.WriteOpenResponse(stream, tunnel.OpenResponse{OK: true}); err != nil {
		_ = upstream.Close()
		return
	}
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(upstream, stream)
		_ = upstream.Close()
		_ = stream.Close()
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(stream, upstream)
		_ = stream.Close()
		_ = upstream.Close()
		done <- struct{}{}
	}()
	<-done
}
