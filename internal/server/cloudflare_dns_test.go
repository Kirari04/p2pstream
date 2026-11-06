package server

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"p2pstream/internal/db"
)

type fakeDNS01Resolver struct {
	nameservers []*net.NS
	addresses   map[string][]net.IPAddr
	nsQueries   []string
	nsErr       error
	ipErr       map[string]error
}

func (r *fakeDNS01Resolver) LookupNS(_ context.Context, name string) ([]*net.NS, error) {
	r.nsQueries = append(r.nsQueries, name)
	return r.nameservers, r.nsErr
}

func (r *fakeDNS01Resolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	if err := r.ipErr[host]; err != nil {
		return nil, err
	}
	return r.addresses[host], nil
}

func TestCloudflareAuthoritiesForDomainValidatesDelegationAndAddresses(t *testing.T) {
	resolver := &fakeDNS01Resolver{
		nameservers: []*net.NS{{Host: "B.NS.Cloudflare.com."}, {Host: "a.ns.cloudflare.com."}},
		addresses: map[string][]net.IPAddr{
			"a.ns.cloudflare.com.": {{IP: net.ParseIP("10.0.0.1")}, {IP: net.ParseIP("1.1.1.1")}},
			"b.ns.cloudflare.com.": {{IP: net.ParseIP("2606:4700:4700::1111")}},
		},
	}
	solverUnderTest := cloudflareDNSSolver{dnsResolver: resolver}
	zone := cloudflareZoneDetails{
		Name:        "example.com",
		Status:      "active",
		Type:        "full",
		NameServers: []string{"a.ns.cloudflare.com", "b.ns.cloudflare.com"},
	}

	authorities, err := solverUnderTest.authoritiesForDomain(context.Background(), "*.sub.example.com", zone)
	if err != nil {
		t.Fatal(err)
	}
	if len(authorities) != 2 || authorities[0].Name != "a.ns.cloudflare.com." || authorities[1].Name != "b.ns.cloudflare.com." {
		t.Fatalf("authorities = %#v", authorities)
	}
	if got := authorities[0].Addresses; len(got) != 1 || got[0] != netip.MustParseAddr("1.1.1.1") {
		t.Fatalf("filtered addresses = %v", got)
	}

	resolver.nameservers = append(resolver.nameservers, &net.NS{Host: "secondary.example.net."})
	resolver.addresses["secondary.example.net."] = []net.IPAddr{{IP: net.ParseIP("9.9.9.9")}}
	authorities, err = solverUnderTest.authoritiesForDomain(context.Background(), "example.com", zone)
	if err != nil {
		t.Fatalf("multi-provider delegation rejected: %v", err)
	}
	if len(authorities) != 3 || authorities[2].Name != "secondary.example.net." {
		t.Fatalf("multi-provider authorities = %#v", authorities)
	}

	idnaZone := zone
	idnaZone.Name = "züribadi.ch"
	if _, err := solverUnderTest.authoritiesForDomain(context.Background(), "xn--zribadi-n2a.ch", idnaZone); err != nil {
		t.Fatalf("IDNA-equivalent Cloudflare zone rejected: %v", err)
	}
	if got := resolver.nsQueries[len(resolver.nsQueries)-1]; got != "xn--zribadi-n2a.ch." {
		t.Fatalf("IDNA zone delegation lookup = %q, want punycode", got)
	}

	if _, err := solverUnderTest.authoritiesForDomain(context.Background(), "*.attacker.example", zone); err == nil || !strings.Contains(err.Error(), "outside configured Cloudflare zone") {
		t.Fatalf("outside-zone error = %v", err)
	}

	zone.NameServers = []string{"other.ns.cloudflare.com"}
	if _, err := solverUnderTest.authoritiesForDomain(context.Background(), "example.com", zone); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("delegation mismatch error = %v", err)
	}
}

func TestCloudflareAuthoritiesForDomainRejectsUnsafeOnlyNameserver(t *testing.T) {
	resolver := &fakeDNS01Resolver{
		nameservers: []*net.NS{{Host: "a.ns.cloudflare.com."}},
		addresses: map[string][]net.IPAddr{
			"a.ns.cloudflare.com.": {{IP: net.ParseIP("127.0.0.1")}, {IP: net.ParseIP("192.168.1.1")}},
		},
	}
	solverUnderTest := cloudflareDNSSolver{dnsResolver: resolver}
	zone := cloudflareZoneDetails{Name: "example.com", NameServers: []string{"a.ns.cloudflare.com"}}

	_, err := solverUnderTest.authoritiesForDomain(context.Background(), "example.com", zone)
	if err == nil || !strings.Contains(err.Error(), "no safe public IP address") {
		t.Fatalf("unsafe-address error = %v", err)
	}
}

func TestCloudflareAuthoritiesForDomainPreservesCancellation(t *testing.T) {
	solverUnderTest := cloudflareDNSSolver{dnsResolver: &fakeDNS01Resolver{nsErr: context.Canceled}}
	zone := cloudflareZoneDetails{Name: "example.com", NameServers: []string{"a.ns.cloudflare.com"}}

	_, err := solverUnderTest.authoritiesForDomain(context.Background(), "example.com", zone)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("delegation cancellation error = %v", err)
	}
}

func TestWaitForAuthoritativeTXTRequiresEveryNameserver(t *testing.T) {
	const token = "sensitive-challenge-token"
	authorities := []dns01Authority{
		{Name: "a.ns.example.", Addresses: []netip.Addr{netip.MustParseAddr("1.1.1.1")}},
		{Name: "b.ns.example.", Addresses: []netip.Addr{netip.MustParseAddr("8.8.8.8")}},
	}
	solverUnderTest := cloudflareDNSSolver{
		pollTimeout:     15 * time.Millisecond,
		pollInterval:    time.Millisecond,
		exchangeTimeout: 5 * time.Millisecond,
		authorityQuery: func(_ context.Context, endpoint string, _ string, _ string) error {
			if strings.HasPrefix(endpoint, "8.8.8.8:") {
				return errors.New("expected TXT value is absent")
			}
			return nil
		},
	}
	err := solverUnderTest.waitForAuthoritativeTXT(context.Background(), "_acme-challenge.example.com", token, authorities)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error = %v", err)
	}
	if !strings.Contains(err.Error(), "b.ns.example.") {
		t.Fatalf("timeout lacks failing nameserver: %v", err)
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("timeout leaked challenge token: %v", err)
	}

	solverUnderTest.authorityQuery = func(context.Context, string, string, string) error { return nil }
	if err := solverUnderTest.waitForAuthoritativeTXT(context.Background(), "_acme-challenge.example.com", token, authorities); err != nil {
		t.Fatal(err)
	}
}

func TestValidateAuthoritativeTXTResponse(t *testing.T) {
	const (
		queryID = 42
		name    = "_acme-challenge.example.com."
		token   = "split-token"
	)
	questionName, err := dnsmessage.NewName(name)
	if err != nil {
		t.Fatal(err)
	}
	question := dnsmessage.Question{Name: questionName, Type: dnsmessage.TypeTXT, Class: dnsmessage.ClassINET}
	answer := dnsmessage.Resource{
		Header: dnsmessage.ResourceHeader{Name: questionName, Type: dnsmessage.TypeTXT, Class: dnsmessage.ClassINET, TTL: 60},
		Body:   &dnsmessage.TXTResource{TXT: []string{"split-", "token"}},
	}
	base := dnsmessage.Message{
		Header:    dnsmessage.Header{ID: queryID, Response: true, Authoritative: true, RCode: dnsmessage.RCodeSuccess},
		Questions: []dnsmessage.Question{question},
		Answers:   []dnsmessage.Resource{answer},
	}

	pack := func(t *testing.T, message dnsmessage.Message) []byte {
		t.Helper()
		packet, err := message.Pack()
		if err != nil {
			t.Fatal(err)
		}
		return packet
	}
	if err := validateAuthoritativeTXTResponse(pack(t, base), queryID, question, token); err != nil {
		t.Fatalf("split TXT rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*dnsmessage.Message)
	}{
		{name: "non-authoritative", mutate: func(message *dnsmessage.Message) { message.Authoritative = false }},
		{name: "NXDOMAIN", mutate: func(message *dnsmessage.Message) { message.RCode = dnsmessage.RCodeNameError }},
		{name: "wrong class", mutate: func(message *dnsmessage.Message) { message.Answers[0].Header.Class = dnsmessage.ClassCHAOS }},
		{name: "separate TXT records are not concatenated", mutate: func(message *dnsmessage.Message) {
			message.Answers = []dnsmessage.Resource{
				{Header: answer.Header, Body: &dnsmessage.TXTResource{TXT: []string{"split-"}}},
				{Header: answer.Header, Body: &dnsmessage.TXTResource{TXT: []string{"token"}}},
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			message := base
			message.Questions = append([]dnsmessage.Question(nil), base.Questions...)
			message.Answers = append([]dnsmessage.Resource(nil), base.Answers...)
			tc.mutate(&message)
			if err := validateAuthoritativeTXTResponse(pack(t, message), queryID, question, token); err == nil {
				t.Fatal("invalid response accepted")
			}
		})
	}
}

func TestQueryAuthoritativeTXTRetriesTruncatedUDPOverTCP(t *testing.T) {
	tcpListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcpListener.Close()
	port := tcpListener.Addr().(*net.TCPAddr).Port
	udpConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port})
	if err != nil {
		t.Fatal(err)
	}
	defer udpConn.Close()

	const (
		name  = "_acme-challenge.example.com"
		token = "expected-token"
	)
	serverErrors := make(chan error, 2)
	go func() {
		buffer := make([]byte, 2048)
		read, peer, readErr := udpConn.ReadFromUDP(buffer)
		if readErr != nil {
			serverErrors <- readErr
			return
		}
		query, unpackErr := unpackDNSMessage(buffer[:read])
		if unpackErr != nil {
			serverErrors <- unpackErr
			return
		}
		response, packErr := (&dnsmessage.Message{
			Header:    dnsmessage.Header{ID: query.ID, Response: true, Authoritative: true, Truncated: true},
			Questions: query.Questions,
		}).Pack()
		if packErr == nil {
			_, packErr = udpConn.WriteToUDP(response, peer)
		}
		serverErrors <- packErr
	}()
	go func() {
		conn, acceptErr := tcpListener.Accept()
		if acceptErr != nil {
			serverErrors <- acceptErr
			return
		}
		defer conn.Close()
		var lengthBytes [2]byte
		if _, acceptErr = io.ReadFull(conn, lengthBytes[:]); acceptErr != nil {
			serverErrors <- acceptErr
			return
		}
		packet := make([]byte, int(binary.BigEndian.Uint16(lengthBytes[:])))
		if _, acceptErr = io.ReadFull(conn, packet); acceptErr != nil {
			serverErrors <- acceptErr
			return
		}
		query, unpackErr := unpackDNSMessage(packet)
		if unpackErr != nil {
			serverErrors <- unpackErr
			return
		}
		response, packErr := authoritativeTXTResponse(query, token)
		if packErr != nil {
			serverErrors <- packErr
			return
		}
		frame := make([]byte, 2+len(response))
		binary.BigEndian.PutUint16(frame[:2], uint16(len(response)))
		copy(frame[2:], response)
		_, packErr = io.Copy(conn, strings.NewReader(string(frame)))
		serverErrors <- packErr
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	endpoint := net.JoinHostPort("127.0.0.1", fmt.Sprint(port))
	if err := queryAuthoritativeTXT(ctx, endpoint, name, token); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := <-serverErrors; err != nil {
			t.Fatal(err)
		}
	}
}

func TestQueryAuthoritativeTXTPreservesCancellation(t *testing.T) {
	udpConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer udpConn.Close()

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(10*time.Millisecond, cancel)
	err = queryAuthoritativeTXT(ctx, udpConn.LocalAddr().String(), "_acme-challenge.example.com", "token")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func TestCloudflarePresentUsesAuthoritativeDNSAndIdempotentCleanup(t *testing.T) {
	var deletes atomic.Int64
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/zones/zone-id":
			_, _ = io.WriteString(w, `{"success":true,"result":{"id":"zone-id","name":"example.com","status":"active","type":"full","name_servers":["a.ns.cloudflare.com","b.ns.cloudflare.com"]}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/zones/zone-id/dns_records":
			_, _ = io.WriteString(w, `{"success":true,"result":{"id":"record-id"}}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/zones/zone-id/dns_records/record-id":
			deletes.Add(1)
			_, _ = io.WriteString(w, `{"success":true,"result":{"id":"record-id"}}`)
		default:
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}))
	defer api.Close()

	resolver := &fakeDNS01Resolver{
		nameservers: []*net.NS{{Host: "a.ns.cloudflare.com."}, {Host: "b.ns.cloudflare.com."}},
		addresses: map[string][]net.IPAddr{
			"a.ns.cloudflare.com.": {{IP: net.ParseIP("1.1.1.1")}},
			"b.ns.cloudflare.com.": {{IP: net.ParseIP("8.8.8.8")}},
		},
	}
	var authorityQueries atomic.Int64
	solverUnderTest := cloudflareDNSSolver{
		credential:     db.PublicTlsDnsCredential{CloudflareZoneID: "zone-id", ApiToken: "token"},
		httpClient:     api.Client(),
		apiBaseURL:     api.URL,
		dnsResolver:    resolver,
		pollTimeout:    50 * time.Millisecond,
		pollInterval:   time.Millisecond,
		authorityQuery: func(context.Context, string, string, string) error { authorityQueries.Add(1); return nil },
	}
	cleanup, err := solverUnderTest.Present(context.Background(), "*.example.com", "challenge-token")
	if err != nil {
		t.Fatal(err)
	}
	if authorityQueries.Load() != 2 {
		t.Fatalf("authority queries = %d, want 2", authorityQueries.Load())
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	if deletes.Load() != 1 {
		t.Fatalf("deletes = %d, want 1", deletes.Load())
	}
}

func TestCloudflarePresentReportsPropagationAndCleanupFailures(t *testing.T) {
	var deletes atomic.Int64
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/zones/zone-id":
			_, _ = io.WriteString(w, `{"success":true,"result":{"id":"zone-id","name":"example.com","status":"active","type":"full","name_servers":["a.ns.cloudflare.com"]}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/zones/zone-id/dns_records":
			_, _ = io.WriteString(w, `{"success":true,"result":{"id":"record-id"}}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/zones/zone-id/dns_records/record-id":
			deletes.Add(1)
			http.Error(w, "cleanup rejected", http.StatusBadGateway)
		default:
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}))
	defer api.Close()

	resolver := &fakeDNS01Resolver{
		nameservers: []*net.NS{{Host: "a.ns.cloudflare.com."}},
		addresses:   map[string][]net.IPAddr{"a.ns.cloudflare.com.": {{IP: net.ParseIP("1.1.1.1")}}},
	}
	const token = "challenge-token-must-not-leak"
	solverUnderTest := cloudflareDNSSolver{
		credential:      db.PublicTlsDnsCredential{CloudflareZoneID: "zone-id", ApiToken: "token"},
		httpClient:      api.Client(),
		apiBaseURL:      api.URL,
		dnsResolver:     resolver,
		pollTimeout:     10 * time.Millisecond,
		pollInterval:    time.Millisecond,
		exchangeTimeout: time.Millisecond,
		authorityQuery:  func(context.Context, string, string, string) error { return errors.New("TXT absent") },
	}
	_, err := solverUnderTest.Present(context.Background(), "*.example.com", token)
	if err == nil || !strings.Contains(err.Error(), "TXT absent") || !strings.Contains(err.Error(), "cleanup rejected") {
		t.Fatalf("joined propagation/cleanup error = %v", err)
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("failure leaked challenge token: %v", err)
	}
	if deletes.Load() != 1 {
		t.Fatalf("deletes = %d, want 1", deletes.Load())
	}
}

func unpackDNSMessage(packet []byte) (dnsmessage.Message, error) {
	var message dnsmessage.Message
	err := message.Unpack(packet)
	return message, err
}

func authoritativeTXTResponse(query dnsmessage.Message, token string) ([]byte, error) {
	if len(query.Questions) != 1 {
		return nil, errors.New("unexpected question count")
	}
	question := query.Questions[0]
	return (&dnsmessage.Message{
		Header:    dnsmessage.Header{ID: query.ID, Response: true, Authoritative: true, RCode: dnsmessage.RCodeSuccess},
		Questions: query.Questions,
		Answers: []dnsmessage.Resource{{
			Header: dnsmessage.ResourceHeader{Name: question.Name, Type: dnsmessage.TypeTXT, Class: dnsmessage.ClassINET, TTL: 60},
			Body:   &dnsmessage.TXTResource{TXT: []string{token}},
		}},
	}).Pack()
}
