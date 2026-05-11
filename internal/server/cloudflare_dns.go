package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"p2pstream/internal/db"
)

const (
	cloudflareAPIBaseURL      = "https://api.cloudflare.com/client/v4"
	cloudflareDNSPollTimeout  = 2 * time.Minute
	cloudflareDNSPollInterval = 5 * time.Second
)

type cloudflareDNSSolver struct {
	credential db.PublicTlsDnsCredential
	httpClient *http.Client
}

type cloudflareCreateDNSRecordRequest struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int64  `json:"ttl"`
}

type cloudflareDNSRecordResponse struct {
	Success bool `json:"success"`
	Result  struct {
		ID string `json:"id"`
	} `json:"result"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

func (s cloudflareDNSSolver) Present(ctx context.Context, domain string, value string) (func(), error) {
	recordName := acmeDNS01RecordName(domain)
	recordID, err := s.createTXTRecord(ctx, recordName, value)
	if err != nil {
		return nil, err
	}
	cleanup := func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = s.deleteRecord(cleanupCtx, recordID)
	}
	if err := waitForDNS01TXT(ctx, recordName, value); err != nil {
		cleanup()
		return nil, err
	}
	return cleanup, nil
}

func (s cloudflareDNSSolver) createTXTRecord(ctx context.Context, name string, value string) (string, error) {
	payload, err := json.Marshal(cloudflareCreateDNSRecordRequest{
		Type:    "TXT",
		Name:    name,
		Content: value,
		TTL:     120,
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.recordsURL(), bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+s.credential.ApiToken)
	req.Header.Set("Content-Type", "application/json")

	var resp cloudflareDNSRecordResponse
	if err := s.doJSON(req, &resp); err != nil {
		return "", err
	}
	if !resp.Success || resp.Result.ID == "" {
		return "", fmt.Errorf("Cloudflare rejected DNS record create: %s", cloudflareErrorMessages(resp))
	}
	return resp.Result.ID, nil
}

func (s cloudflareDNSSolver) deleteRecord(ctx context.Context, recordID string) error {
	if recordID == "" {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, s.recordsURL()+"/"+recordID, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.credential.ApiToken)
	var resp cloudflareDNSRecordResponse
	if err := s.doJSON(req, &resp); err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("Cloudflare rejected DNS record delete: %s", cloudflareErrorMessages(resp))
	}
	return nil
}

func (s cloudflareDNSSolver) recordsURL() string {
	return cloudflareAPIBaseURL + "/zones/" + s.credential.CloudflareZoneID + "/dns_records"
}

func (s cloudflareDNSSolver) doJSON(req *http.Request, target any) error {
	client := s.httpClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Cloudflare API returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, target); err != nil {
		return err
	}
	return nil
}

func waitForDNS01TXT(ctx context.Context, name string, value string) error {
	ctx, cancel := context.WithTimeout(ctx, cloudflareDNSPollTimeout)
	defer cancel()
	ticker := time.NewTicker(cloudflareDNSPollInterval)
	defer ticker.Stop()
	for {
		records, _ := net.DefaultResolver.LookupTXT(ctx, name)
		for _, record := range records {
			if record == value {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("DNS TXT record %s did not propagate before timeout", name)
		case <-ticker.C:
		}
	}
}

func acmeDNS01RecordName(domain string) string {
	domain = strings.TrimSuffix(strings.TrimPrefix(normalizeHostPattern(domain), "*."), ".")
	return "_acme-challenge." + domain
}

func cloudflareErrorMessages(resp cloudflareDNSRecordResponse) string {
	if len(resp.Errors) == 0 {
		return "unknown error"
	}
	messages := make([]string, 0, len(resp.Errors))
	for _, item := range resp.Errors {
		if item.Message != "" {
			messages = append(messages, item.Message)
		}
	}
	if len(messages) == 0 {
		return "unknown error"
	}
	return strings.Join(messages, "; ")
}
