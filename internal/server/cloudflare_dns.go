package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"p2pstream/internal/db"
)

const (
	cloudflareAPIBaseURL           = "https://api.cloudflare.com/client/v4"
	cloudflareDNSPollTimeout       = 5 * time.Minute
	cloudflareDNSPollInterval      = 5 * time.Second
	cloudflareDNSExchangeTimeout   = 5 * time.Second
	cloudflareDNSCleanupTimeout    = 30 * time.Second
	cloudflareDNSMaxNameServers    = 8
	cloudflareDNSMaxServerIPs      = 8
	cloudflareDNSAuthoritativePort = "53"
)

type dns01Resolver interface {
	LookupNS(context.Context, string) ([]*net.NS, error)
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

type dns01Authority struct {
	Name      string
	Addresses []netip.Addr
}

type cloudflareDNSSolver struct {
	credential      db.PublicTlsDnsCredential
	httpClient      *http.Client
	dnsResolver     dns01Resolver
	authorityQuery  func(context.Context, string, string, string) error
	apiBaseURL      string
	pollTimeout     time.Duration
	pollInterval    time.Duration
	exchangeTimeout time.Duration
}

type cloudflareCreateDNSRecordRequest struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int64  `json:"ttl"`
}

type cloudflareAPIError struct {
	Message string `json:"message"`
}

type cloudflareDNSRecordResponse struct {
	Success bool `json:"success"`
	Result  struct {
		ID string `json:"id"`
	} `json:"result"`
	Errors []cloudflareAPIError `json:"errors"`
}

type cloudflareZoneResponse struct {
	Success bool `json:"success"`
	Result  struct {
		ID                string   `json:"id"`
		Name              string   `json:"name"`
		Status            string   `json:"status"`
		Type              string   `json:"type"`
		NameServers       []string `json:"name_servers"`
		VanityNameServers []string `json:"vanity_name_servers"`
	} `json:"result"`
	Errors []cloudflareAPIError `json:"errors"`
}

type cloudflareZoneDetails struct {
	Name              string
	Status            string
	Type              string
	NameServers       []string
	VanityNameServers []string
}

func (s cloudflareDNSSolver) Present(ctx context.Context, domain string, value string) (func() error, error) {
	zone, err := s.getZoneDetails(ctx)
	if err != nil {
		return nil, err
	}
	authorities, err := s.authoritiesForDomain(ctx, domain, zone)
	if err != nil {
		return nil, err
	}

	recordName := acmeDNS01RecordName(domain)
	recordID, err := s.createTXTRecord(ctx, recordName, value)
	if err != nil {
		return nil, err
	}
	cleanup := s.recordCleanup(recordID)
	if err := s.waitForAuthoritativeTXT(ctx, recordName, value, authorities); err != nil {
		if cleanupErr := cleanup(); cleanupErr != nil {
			return nil, errors.Join(err, fmt.Errorf("delete unpropagated Cloudflare DNS challenge record: %w", cleanupErr))
		}
		return nil, err
	}
	return cleanup, nil
}

func (s cloudflareDNSSolver) getZoneDetails(ctx context.Context) (cloudflareZoneDetails, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.zoneURL(), nil)
	if err != nil {
		return cloudflareZoneDetails{}, err
	}
	req.Header.Set("Authorization", "Bearer "+s.credential.ApiToken)

	var resp cloudflareZoneResponse
	if err := s.doJSON(req, &resp); err != nil {
		return cloudflareZoneDetails{}, err
	}
	if !resp.Success || resp.Result.ID == "" {
		return cloudflareZoneDetails{}, fmt.Errorf("Cloudflare rejected zone lookup: %s", cloudflareErrorMessages(resp.Errors))
	}
	zone := cloudflareZoneDetails{
		Name:              resp.Result.Name,
		Status:            strings.ToLower(strings.TrimSpace(resp.Result.Status)),
		Type:              strings.ToLower(strings.TrimSpace(resp.Result.Type)),
		NameServers:       append([]string(nil), resp.Result.NameServers...),
		VanityNameServers: append([]string(nil), resp.Result.VanityNameServers...),
	}
	if zone.Status != "active" {
		return cloudflareZoneDetails{}, fmt.Errorf("Cloudflare zone %q is not active (status %q)", zone.Name, zone.Status)
	}
	if zone.Type == "internal" || zone.Type == "partial" {
		return cloudflareZoneDetails{}, fmt.Errorf("Cloudflare zone %q type %q is not authoritative public DNS", zone.Name, zone.Type)
	}
	if len(zone.NameServers) == 0 && len(zone.VanityNameServers) == 0 {
		return cloudflareZoneDetails{}, fmt.Errorf("Cloudflare zone %q has no authoritative nameservers", zone.Name)
	}
	return zone, nil
}

func (s cloudflareDNSSolver) authoritiesForDomain(ctx context.Context, domain string, zone cloudflareZoneDetails) ([]dns01Authority, error) {
	domainHost, err := normalizePublicSiteRequestDNSName(strings.TrimPrefix(normalizeHostPattern(domain), "*."))
	if err != nil {
		return nil, fmt.Errorf("invalid ACME domain: %w", err)
	}
	domainDNSName, err := canonicalDNSName(domainHost)
	if err != nil {
		return nil, fmt.Errorf("invalid ACME domain: %w", err)
	}
	zoneHost, err := normalizePublicSiteRequestDNSName(zone.Name)
	if err != nil {
		return nil, fmt.Errorf("Cloudflare returned invalid zone name: %w", err)
	}
	zoneDNSName, err := canonicalDNSName(zoneHost)
	if err != nil {
		return nil, fmt.Errorf("Cloudflare returned invalid zone name: %w", err)
	}
	domainName := strings.TrimSuffix(domainDNSName, ".")
	zoneName := strings.TrimSuffix(zoneDNSName, ".")
	if domainName != zoneName && !strings.HasSuffix(domainName, "."+zoneName) {
		return nil, fmt.Errorf("ACME domain %q is outside configured Cloudflare zone %q", domainName, zoneName)
	}

	resolver := s.resolver()
	delegation, err := resolver.LookupNS(ctx, zoneDNSName)
	if err != nil {
		return nil, fmt.Errorf("look up public DNS delegation for %s: %w", zoneName, err)
	}
	actualNames, err := normalizedNSHosts(delegation)
	if err != nil {
		return nil, fmt.Errorf("invalid public DNS delegation for %s: %w", zoneName, err)
	}
	if len(actualNames) == 0 {
		return nil, fmt.Errorf("public DNS delegation for %s has no nameservers", zoneName)
	}
	if len(actualNames) > cloudflareDNSMaxNameServers {
		return nil, fmt.Errorf("public DNS delegation for %s has too many nameservers (%d, maximum %d)", zoneName, len(actualNames), cloudflareDNSMaxNameServers)
	}

	assignedNames, err := normalizedDNSNames(zone.NameServers)
	if err != nil {
		return nil, fmt.Errorf("Cloudflare returned invalid assigned nameservers: %w", err)
	}
	vanityNames, err := normalizedDNSNames(zone.VanityNameServers)
	if err != nil {
		return nil, fmt.Errorf("Cloudflare returned invalid vanity nameservers: %w", err)
	}
	if !containsDNSNameSet(actualNames, assignedNames) && !containsDNSNameSet(actualNames, vanityNames) {
		return nil, fmt.Errorf("public DNS delegation for %s does not match the nameservers assigned to configured Cloudflare zone", zoneName)
	}

	authorities := make([]dns01Authority, 0, len(actualNames))
	for _, name := range actualNames {
		addresses, lookupErr := resolver.LookupIPAddr(ctx, name)
		if lookupErr != nil {
			return nil, fmt.Errorf("resolve authoritative nameserver %s: %w", name, lookupErr)
		}
		if len(addresses) > cloudflareDNSMaxServerIPs {
			return nil, fmt.Errorf("authoritative nameserver %s has too many addresses (%d, maximum %d)", name, len(addresses), cloudflareDNSMaxServerIPs)
		}
		publicAddresses := make([]netip.Addr, 0, len(addresses))
		seenAddresses := make(map[netip.Addr]struct{}, len(addresses))
		for _, item := range addresses {
			if item.Zone != "" {
				continue
			}
			address, ok := netip.AddrFromSlice(item.IP)
			if !ok {
				continue
			}
			address = address.Unmap()
			if !isPublicGeoIPAddress(address) {
				continue
			}
			if _, exists := seenAddresses[address]; exists {
				continue
			}
			seenAddresses[address] = struct{}{}
			publicAddresses = append(publicAddresses, address)
		}
		if len(publicAddresses) == 0 {
			return nil, fmt.Errorf("authoritative nameserver %s has no safe public IP address", name)
		}
		sort.Slice(publicAddresses, func(i, j int) bool { return publicAddresses[i].Less(publicAddresses[j]) })
		authorities = append(authorities, dns01Authority{Name: name, Addresses: publicAddresses})
	}
	return authorities, nil
}

func normalizedNSHosts(records []*net.NS) ([]string, error) {
	names := make([]string, 0, len(records))
	for _, record := range records {
		if record == nil {
			continue
		}
		names = append(names, record.Host)
	}
	return normalizedDNSNames(names)
}

func normalizedDNSNames(names []string) ([]string, error) {
	result := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, raw := range names {
		name, err := canonicalDNSName(raw)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		result = append(result, name)
	}
	sort.Strings(result)
	return result, nil
}

func containsDNSNameSet(haystack []string, needles []string) bool {
	if len(needles) == 0 || len(haystack) < len(needles) {
		return false
	}
	available := make(map[string]struct{}, len(haystack))
	for _, name := range haystack {
		available[name] = struct{}{}
	}
	for _, name := range needles {
		if _, ok := available[name]; !ok {
			return false
		}
	}
	return true
}

func canonicalDNSName(raw string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(raw))
	if name == "" {
		return "", errors.New("DNS name is empty")
	}
	if !strings.HasSuffix(name, ".") {
		name += "."
	}
	parsed, err := dnsmessage.NewName(name)
	if err != nil {
		return "", fmt.Errorf("invalid DNS name %q: %w", raw, err)
	}
	return strings.ToLower(parsed.String()), nil
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
		return "", fmt.Errorf("Cloudflare rejected DNS record create: %s", cloudflareErrorMessages(resp.Errors))
	}
	return resp.Result.ID, nil
}

func (s cloudflareDNSSolver) recordCleanup(recordID string) func() error {
	var once sync.Once
	var cleanupErr error
	return func() error {
		once.Do(func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), cloudflareDNSCleanupTimeout)
			defer cancel()
			cleanupErr = s.deleteRecord(cleanupCtx, recordID)
		})
		return cleanupErr
	}
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
		return fmt.Errorf("Cloudflare rejected DNS record delete: %s", cloudflareErrorMessages(resp.Errors))
	}
	return nil
}

func (s cloudflareDNSSolver) zoneURL() string {
	return s.baseURL() + "/zones/" + s.credential.CloudflareZoneID
}

func (s cloudflareDNSSolver) recordsURL() string {
	return s.zoneURL() + "/dns_records"
}

func (s cloudflareDNSSolver) baseURL() string {
	if strings.TrimSpace(s.apiBaseURL) != "" {
		return strings.TrimRight(s.apiBaseURL, "/")
	}
	return cloudflareAPIBaseURL
}

func (s cloudflareDNSSolver) resolver() dns01Resolver {
	if s.dnsResolver != nil {
		return s.dnsResolver
	}
	return net.DefaultResolver
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

func (s cloudflareDNSSolver) waitForAuthoritativeTXT(ctx context.Context, name string, value string, authorities []dns01Authority) error {
	timeout := s.pollTimeout
	if timeout <= 0 {
		timeout = cloudflareDNSPollTimeout
	}
	interval := s.pollInterval
	if interval <= 0 {
		interval = cloudflareDNSPollInterval
	}
	pollCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var lastErr error
	for {
		if err := s.checkAuthoritativeTXT(pollCtx, name, value, authorities); err == nil {
			return nil
		} else {
			lastErr = err
		}

		timer := time.NewTimer(interval)
		select {
		case <-pollCtx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			if lastErr != nil {
				return fmt.Errorf("DNS TXT record %s was not authoritative on all configured Cloudflare nameservers: %v: %w", name, lastErr, pollCtx.Err())
			}
			return fmt.Errorf("DNS TXT record %s did not become authoritative: %w", name, pollCtx.Err())
		case <-timer.C:
		}
	}
}

func (s cloudflareDNSSolver) checkAuthoritativeTXT(ctx context.Context, name string, value string, authorities []dns01Authority) error {
	if len(authorities) == 0 {
		return errors.New("no authoritative nameservers available")
	}
	type result struct {
		name string
		err  error
	}
	results := make(chan result, len(authorities))
	for _, authority := range authorities {
		authority := authority
		go func() {
			results <- result{name: authority.Name, err: s.checkOneAuthority(ctx, name, value, authority)}
		}()
	}

	failures := make([]string, 0, len(authorities))
	for range authorities {
		item := <-results
		if item.err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", item.name, item.err))
		}
	}
	if len(failures) == 0 {
		return nil
	}
	sort.Strings(failures)
	return errors.New(strings.Join(failures, "; "))
}

func (s cloudflareDNSSolver) checkOneAuthority(ctx context.Context, name string, value string, authority dns01Authority) error {
	query := s.authorityQuery
	if query == nil {
		query = queryAuthoritativeTXT
	}
	exchangeTimeout := s.exchangeTimeout
	if exchangeTimeout <= 0 {
		exchangeTimeout = cloudflareDNSExchangeTimeout
	}
	var lastErr error
	for _, address := range authority.Addresses {
		queryCtx, cancel := context.WithTimeout(ctx, exchangeTimeout)
		endpoint := net.JoinHostPort(address.String(), cloudflareDNSAuthoritativePort)
		err := query(queryCtx, endpoint, name, value)
		cancel()
		if err == nil {
			return nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	if lastErr == nil {
		return errors.New("no safe public nameserver address available")
	}
	return fmt.Errorf("no address returned the expected authoritative TXT answer: %w", lastErr)
}

func queryAuthoritativeTXT(ctx context.Context, endpoint string, name string, value string) error {
	canonicalName, err := canonicalDNSName(name)
	if err != nil {
		return err
	}
	questionName, err := dnsmessage.NewName(canonicalName)
	if err != nil {
		return err
	}
	var idBytes [2]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		return fmt.Errorf("generate DNS transaction ID: %w", err)
	}
	queryID := binary.BigEndian.Uint16(idBytes[:])
	question := dnsmessage.Question{Name: questionName, Type: dnsmessage.TypeTXT, Class: dnsmessage.ClassINET}
	query, err := (&dnsmessage.Message{
		Header:    dnsmessage.Header{ID: queryID, RecursionDesired: false},
		Questions: []dnsmessage.Question{question},
	}).Pack()
	if err != nil {
		return fmt.Errorf("pack authoritative DNS query: %w", err)
	}

	response, err := exchangeDNSPacket(ctx, "udp", endpoint, query)
	if err != nil {
		return fmt.Errorf("authoritative DNS UDP exchange with %s: %w", endpoint, err)
	}
	var parser dnsmessage.Parser
	header, err := parser.Start(response)
	if err != nil {
		return fmt.Errorf("parse authoritative DNS UDP header from %s: %w", endpoint, err)
	}
	if header.ID != queryID || !header.Response || header.OpCode != 0 {
		return fmt.Errorf("invalid authoritative DNS UDP response from %s", endpoint)
	}
	if header.Truncated {
		response, err = exchangeDNSPacket(ctx, "tcp", endpoint, query)
		if err != nil {
			return fmt.Errorf("authoritative DNS TCP retry with %s: %w", endpoint, err)
		}
	}
	return validateAuthoritativeTXTResponse(response, queryID, question, value)
}

func exchangeDNSPacket(ctx context.Context, network string, endpoint string, query []byte) ([]byte, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, network, endpoint)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return nil, err
		}
	}
	stopCancel := context.AfterFunc(ctx, func() {
		_ = conn.SetDeadline(time.Now())
	})
	defer stopCancel()

	if network == "tcp" {
		if len(query) > int(^uint16(0)) {
			return nil, errors.New("DNS query is too large")
		}
		frame := make([]byte, 2+len(query))
		binary.BigEndian.PutUint16(frame[:2], uint16(len(query)))
		copy(frame[2:], query)
		if _, err := io.Copy(conn, bytes.NewReader(frame)); err != nil {
			return nil, dnsExchangeError(ctx, err)
		}
		var lengthBytes [2]byte
		if _, err := io.ReadFull(conn, lengthBytes[:]); err != nil {
			return nil, dnsExchangeError(ctx, err)
		}
		length := int(binary.BigEndian.Uint16(lengthBytes[:]))
		if length == 0 {
			return nil, errors.New("empty DNS TCP response")
		}
		response := make([]byte, length)
		if _, err := io.ReadFull(conn, response); err != nil {
			return nil, dnsExchangeError(ctx, err)
		}
		return response, nil
	}

	written, err := conn.Write(query)
	if err != nil {
		return nil, dnsExchangeError(ctx, err)
	}
	if written != len(query) {
		return nil, io.ErrShortWrite
	}
	response := make([]byte, 65535)
	read, err := conn.Read(response)
	if err != nil {
		return nil, dnsExchangeError(ctx, err)
	}
	return response[:read], nil
}

func dnsExchangeError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return err
}

func validateAuthoritativeTXTResponse(response []byte, queryID uint16, question dnsmessage.Question, value string) error {
	var message dnsmessage.Message
	if err := message.Unpack(response); err != nil {
		return fmt.Errorf("unpack authoritative DNS response: %w", err)
	}
	if message.ID != queryID || !message.Response || message.OpCode != 0 {
		return errors.New("DNS response does not match query")
	}
	if message.Truncated {
		return errors.New("authoritative DNS response remained truncated after TCP retry")
	}
	if len(message.Questions) != 1 || !sameDNSQuestion(message.Questions[0], question) {
		return errors.New("DNS response question does not match query")
	}
	if message.RCode != dnsmessage.RCodeSuccess {
		return fmt.Errorf("authoritative DNS response code %s", message.RCode.String())
	}
	if !message.Authoritative {
		return errors.New("DNS response is not authoritative")
	}
	expectedName := strings.ToLower(question.Name.String())
	for _, answer := range message.Answers {
		if strings.ToLower(answer.Header.Name.String()) != expectedName || answer.Header.Type != dnsmessage.TypeTXT || answer.Header.Class != dnsmessage.ClassINET {
			continue
		}
		txt, ok := answer.Body.(*dnsmessage.TXTResource)
		if ok && strings.Join(txt.TXT, "") == value {
			return nil
		}
	}
	return errors.New("expected TXT value is absent from authoritative answer")
}

func sameDNSQuestion(left dnsmessage.Question, right dnsmessage.Question) bool {
	return strings.EqualFold(left.Name.String(), right.Name.String()) && left.Type == right.Type && left.Class == right.Class
}

func acmeDNS01RecordName(domain string) string {
	domain = strings.TrimSuffix(strings.TrimPrefix(normalizeHostPattern(domain), "*."), ".")
	return "_acme-challenge." + domain
}

func cloudflareErrorMessages(items []cloudflareAPIError) string {
	if len(items) == 0 {
		return "unknown error"
	}
	messages := make([]string, 0, len(items))
	for _, item := range items {
		if item.Message != "" {
			messages = append(messages, item.Message)
		}
	}
	if len(messages) == 0 {
		return "unknown error"
	}
	return strings.Join(messages, "; ")
}
