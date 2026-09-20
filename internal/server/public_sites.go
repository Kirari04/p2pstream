package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/net/idna"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/db"
)

const maxPublicSiteHosts = 64

const (
	publicSiteHostRolePrimary      = "primary"
	publicSiteHostRoleAlias        = "alias"
	publicSiteHostBehaviorServe    = "serve"
	publicSiteHostBehaviorRedirect = "redirect"
)

type publicSiteHostMutation struct {
	HostnamePattern string
	Role            string
	Behavior        string
}

func normalizePublicSiteHostnamePattern(value string) (string, error) {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.TrimSuffix(value, ".")
	if value == "" {
		return "", errors.New("hostname is required")
	}
	wildcard := strings.HasPrefix(value, "*.")
	if wildcard {
		value = strings.TrimPrefix(value, "*.")
	}
	if strings.Contains(value, "*") || strings.ContainsAny(value, "/\\@[] ") {
		return "", errors.New("hostname must be an exact host or a wildcard like *.example.com")
	}
	if address, parseErr := netip.ParseAddr(value); parseErr == nil {
		if address.Zone() != "" {
			return "", errors.New("IP zone identifiers are not allowed")
		}
		if wildcard {
			return "", errors.New("IP addresses cannot be wildcarded")
		}
		return address.String(), nil
	}
	ascii, err := idna.Lookup.ToASCII(value)
	if err != nil {
		return "", fmt.Errorf("hostname is not valid IDNA: %w", err)
	}
	if len(ascii) > 253 || !validPublicSiteDNSName(ascii) {
		return "", errors.New("hostname is not a valid DNS name")
	}
	if wildcard {
		if !strings.Contains(ascii, ".") {
			return "", errors.New("wildcard hostname must include a registrable suffix")
		}
		return "*." + ascii, nil
	}
	return ascii, nil
}

func normalizePublicSiteRequestDNSName(value string) (string, error) {
	if value == "" || value != strings.TrimSpace(value) || strings.Contains(value, "*") {
		return "", errors.New("invalid hostname")
	}
	value = strings.ToLower(value)
	if strings.HasSuffix(value, ".") {
		value = strings.TrimSuffix(value, ".")
		if strings.HasSuffix(value, ".") {
			return "", errors.New("hostname has multiple trailing dots")
		}
	}
	ascii, err := idna.Lookup.ToASCII(value)
	if err != nil {
		return "", fmt.Errorf("hostname is not valid IDNA: %w", err)
	}
	if len(ascii) == 0 || len(ascii) > 253 {
		return "", errors.New("hostname is not a valid DNS name")
	}
	for _, label := range strings.Split(ascii, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("hostname is not a valid DNS name")
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return "", errors.New("hostname is not a valid DNS name")
			}
		}
	}
	return ascii, nil
}

func validPublicSiteDNSName(value string) bool {
	labels := strings.Split(value, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}
	return true
}

func validatePublicSiteHosts(inputs []*p2pstreamv1.PublicSiteHostInput) ([]publicSiteHostMutation, error) {
	if len(inputs) == 0 || len(inputs) > maxPublicSiteHosts {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("site requires 1-%d hostnames", maxPublicSiteHosts))
	}
	seen := make(map[string]struct{}, len(inputs))
	result := make([]publicSiteHostMutation, 0, len(inputs))
	primaryCount := 0
	for _, input := range inputs {
		if input == nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("site hostname entry is required"))
		}
		hostname, err := normalizePublicSiteHostnamePattern(input.HostnamePattern)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		if _, exists := seen[hostname]; exists {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("duplicate site hostname %q", hostname))
		}
		seen[hostname] = struct{}{}
		behavior := publicSiteHostBehaviorServe
		switch input.Behavior {
		case p2pstreamv1.PublicSiteHostBehavior_PUBLIC_SITE_HOST_BEHAVIOR_UNSPECIFIED,
			p2pstreamv1.PublicSiteHostBehavior_PUBLIC_SITE_HOST_BEHAVIOR_SERVE:
		case p2pstreamv1.PublicSiteHostBehavior_PUBLIC_SITE_HOST_BEHAVIOR_REDIRECT:
			behavior = publicSiteHostBehaviorRedirect
		default:
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("site hostname behavior must be serve or redirect"))
		}
		role := publicSiteHostRoleAlias
		if input.Primary {
			primaryCount++
			role = publicSiteHostRolePrimary
			behavior = publicSiteHostBehaviorServe
			if strings.HasPrefix(hostname, "*.") {
				return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("primary hostname must be exact"))
			}
		}
		result = append(result, publicSiteHostMutation{HostnamePattern: hostname, Role: role, Behavior: behavior})
	}
	if primaryCount != 1 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("site requires exactly one exact primary hostname"))
	}
	return result, nil
}

func (a *App) CreatePublicSite(ctx context.Context, req *connect.Request[p2pstreamv1.CreatePublicSiteRequest]) (*connect.Response[p2pstreamv1.CreatePublicSiteResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	return a.publicConfigService().createPublicSite(ctx, req)
}

func (s *publicConfigService) createPublicSite(ctx context.Context, req *connect.Request[p2pstreamv1.CreatePublicSiteRequest]) (*connect.Response[p2pstreamv1.CreatePublicSiteResponse], error) {
	if _, err := s.db.GetPublicListener(ctx, req.Msg.ListenerId); err != nil {
		return nil, publicDBError(err)
	}
	name, err := normalizePublicName(req.Msg.Name)
	if err != nil {
		return nil, err
	}
	hosts, err := validatePublicSiteHosts(req.Msg.Hosts)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, publicDBError(err)
	}
	defer tx.Rollback()
	q := s.db.Queries.WithTx(tx)
	site, err := q.CreatePublicSite(ctx, db.CreatePublicSiteParams{ListenerID: req.Msg.ListenerId, Name: name, Enabled: boolInt(req.Msg.Enabled)})
	if err != nil {
		return nil, publicDBError(err)
	}
	stored, err := replacePublicSiteHosts(ctx, q, site, hosts)
	if err != nil {
		return nil, publicDBError(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, publicDBError(err)
	}
	if err := s.app.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.CreatePublicSiteResponse{Site: s.publicSiteProtoFromCache(site, stored)}), nil
}

func (a *App) UpdatePublicSite(ctx context.Context, req *connect.Request[p2pstreamv1.UpdatePublicSiteRequest]) (*connect.Response[p2pstreamv1.UpdatePublicSiteResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	return a.publicConfigService().updatePublicSite(ctx, req)
}

func (s *publicConfigService) updatePublicSite(ctx context.Context, req *connect.Request[p2pstreamv1.UpdatePublicSiteRequest]) (*connect.Response[p2pstreamv1.UpdatePublicSiteResponse], error) {
	if _, err := s.db.GetPublicListener(ctx, req.Msg.ListenerId); err != nil {
		return nil, publicDBError(err)
	}
	name, err := normalizePublicName(req.Msg.Name)
	if err != nil {
		return nil, err
	}
	hosts, err := validatePublicSiteHosts(req.Msg.Hosts)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, publicDBError(err)
	}
	defer tx.Rollback()
	q := s.db.Queries.WithTx(tx)
	existing, err := q.GetPublicSite(ctx, req.Msg.Id)
	if err != nil {
		return nil, publicDBError(err)
	}
	if existing.ListenerID != req.Msg.ListenerId {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("a site's listener is immutable; create a new site on the other listener"))
	}
	site, err := q.UpdatePublicSite(ctx, db.UpdatePublicSiteParams{ListenerID: req.Msg.ListenerId, Name: name, Enabled: boolInt(req.Msg.Enabled), ID: req.Msg.Id})
	if err != nil {
		return nil, publicDBError(err)
	}
	stored, err := replacePublicSiteHosts(ctx, q, site, hosts)
	if err != nil {
		return nil, publicDBError(err)
	}
	primary := ""
	for _, host := range hosts {
		if host.Role == publicSiteHostRolePrimary {
			primary = host.HostnamePattern
			break
		}
	}
	if err := q.UpdatePublicRouteHostPatternBySite(ctx, db.UpdatePublicRouteHostPatternBySiteParams{HostPattern: primary, SiteID: sql.NullInt64{Int64: site.ID, Valid: true}}); err != nil {
		return nil, publicDBError(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, publicDBError(err)
	}
	if err := s.app.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.UpdatePublicSiteResponse{Site: s.publicSiteProtoFromCache(site, stored)}), nil
}

func (s *publicConfigService) publicSiteProtoFromCache(site db.PublicSite, hosts []db.PublicSiteHost) *p2pstreamv1.PublicSite {
	rows, _, ok := s.app.cachedPublicConfig()
	if !ok {
		return publicSiteToProto(site, hosts, nil, nil)
	}
	listeners := make(map[int64]db.PublicListener, len(rows.Listeners))
	for _, listener := range rows.Listeners {
		listeners[listener.ID] = listener
	}
	return publicSiteToProto(site, hosts, listeners, rows.TLSCertificates)
}

func replacePublicSiteHosts(ctx context.Context, q *db.Queries, site db.PublicSite, hosts []publicSiteHostMutation) ([]db.PublicSiteHost, error) {
	if err := q.DeletePublicSiteHosts(ctx, site.ID); err != nil {
		return nil, err
	}
	stored := make([]db.PublicSiteHost, 0, len(hosts))
	for _, host := range hosts {
		row, err := q.CreatePublicSiteHost(ctx, db.CreatePublicSiteHostParams{SiteID: site.ID, ListenerID: site.ListenerID, HostnamePattern: host.HostnamePattern, Role: host.Role, Behavior: host.Behavior})
		if err != nil {
			return nil, err
		}
		stored = append(stored, row)
	}
	return stored, nil
}

func (a *App) DeletePublicSite(ctx context.Context, req *connect.Request[p2pstreamv1.DeletePublicSiteRequest]) (*connect.Response[p2pstreamv1.DeletePublicSiteResponse], error) {
	if _, err := a.requireAdmin(ctx, req.Header()); err != nil {
		return nil, err
	}
	return a.publicConfigService().deletePublicSite(ctx, req)
}

func (s *publicConfigService) deletePublicSite(ctx context.Context, req *connect.Request[p2pstreamv1.DeletePublicSiteRequest]) (*connect.Response[p2pstreamv1.DeletePublicSiteResponse], error) {
	if _, err := s.db.GetPublicSite(ctx, req.Msg.Id); err != nil {
		return nil, publicDBError(err)
	}
	count, err := s.db.CountPublicRoutesBySite(ctx, sql.NullInt64{Int64: req.Msg.Id, Valid: true})
	if err != nil {
		return nil, publicDBError(err)
	}
	if count > 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("delete or detach the site's routes first"))
	}
	if err := s.db.DeletePublicSite(ctx, req.Msg.Id); err != nil {
		return nil, publicDBError(err)
	}
	if err := s.app.refreshPublicProxySnapshot(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&p2pstreamv1.DeletePublicSiteResponse{}), nil
}

func publicSiteToProto(site db.PublicSite, hosts []db.PublicSiteHost, listeners map[int64]db.PublicListener, certs []db.PublicTlsCertificate) *p2pstreamv1.PublicSite {
	result := &p2pstreamv1.PublicSite{Id: site.ID, ListenerId: site.ListenerID, Name: site.Name, Enabled: site.Enabled != 0, CreatedAtUnixMillis: site.CreatedAt.UnixMilli(), UpdatedAtUnixMillis: site.UpdatedAt.UnixMilli()}
	protocol := ""
	if listener, ok := listeners[site.ListenerID]; ok {
		protocol = listener.Protocol
	}
	for _, host := range hosts {
		coverage, detail := publicSiteTLSCoverage(protocol, host.ListenerID, host.HostnamePattern, certs)
		behavior := p2pstreamv1.PublicSiteHostBehavior_PUBLIC_SITE_HOST_BEHAVIOR_SERVE
		if host.Behavior == publicSiteHostBehaviorRedirect {
			behavior = p2pstreamv1.PublicSiteHostBehavior_PUBLIC_SITE_HOST_BEHAVIOR_REDIRECT
		}
		result.Hosts = append(result.Hosts, &p2pstreamv1.PublicSiteHost{Id: host.ID, SiteId: host.SiteID, HostnamePattern: host.HostnamePattern, Primary: host.Role == publicSiteHostRolePrimary, Behavior: behavior, TlsCoverage: coverage, TlsDetail: detail, CreatedAtUnixMillis: host.CreatedAt.UnixMilli(), UpdatedAtUnixMillis: host.UpdatedAt.UnixMilli()})
	}
	return result
}

func publicSitesToProto(sites []db.PublicSite, hosts []db.PublicSiteHost, listeners []db.PublicListener, certs []db.PublicTlsCertificate) []*p2pstreamv1.PublicSite {
	hostsBySite := make(map[int64][]db.PublicSiteHost)
	for _, host := range hosts {
		hostsBySite[host.SiteID] = append(hostsBySite[host.SiteID], host)
	}
	listenersByID := make(map[int64]db.PublicListener)
	for _, listener := range listeners {
		listenersByID[listener.ID] = listener
	}
	result := make([]*p2pstreamv1.PublicSite, 0, len(sites))
	for _, site := range sites {
		result = append(result, publicSiteToProto(site, hostsBySite[site.ID], listenersByID, certs))
	}
	return result
}

func publicSiteTLSCoverage(protocol string, listenerID int64, hostname string, certs []db.PublicTlsCertificate) (p2pstreamv1.PublicSiteTlsCoverage, string) {
	if protocol != publicListenerProtocolHTTPS {
		return p2pstreamv1.PublicSiteTlsCoverage_PUBLIC_SITE_TLS_COVERAGE_NOT_APPLICABLE, "HTTP listener"
	}
	if addr, err := netip.ParseAddr(hostname); err == nil && addr.Zone() == "" {
		return p2pstreamv1.PublicSiteTlsCoverage_PUBLIC_SITE_TLS_COVERAGE_MISSING, "IP hosts cannot select an HTTPS certificate with SNI; use a DNS hostname"
	}
	var exact *db.PublicTlsCertificate
	var wildcards []*db.PublicTlsCertificate
	for i := range certs {
		certRow := &certs[i]
		if certRow.ListenerID != listenerID || certRow.Enabled == 0 {
			continue
		}
		pattern := normalizeHostPattern(certRow.HostnamePattern)
		if !strictPublicSiteHostMatches(hostname, pattern) {
			continue
		}
		if normalizePublicTLSCertificateSource(certRow.Source) == publicTLSCertificateSourceACME && (certRow.CertPath == "" || certRow.KeyPath == "") {
			continue
		}
		if normalizePublicTLSCertificateSource(certRow.Source) == publicTLSCertificateSourceACME {
			if _, err := os.Stat(certRow.CertPath); errors.Is(err, os.ErrNotExist) {
				continue
			}
			if _, err := os.Stat(certRow.KeyPath); errors.Is(err, os.ErrNotExist) {
				continue
			}
		}
		if strings.HasPrefix(pattern, "*.") {
			wildcards = append(wildcards, certRow)
			continue
		}
		// The live selector builds an exact-name map in query order, so the
		// last duplicate mapping wins. Preserve that behavior here.
		exact = certRow
	}
	// The live selector stable-sorts wildcards by specificity. Equal patterns
	// therefore retain query order (listener, pattern, ID), making the first
	// duplicate mapping effective.
	sort.SliceStable(wildcards, func(i, j int) bool {
		return len(normalizeHostPattern(wildcards[i].HostnamePattern)) > len(normalizeHostPattern(wildcards[j].HostnamePattern))
	})
	selected := exact
	if selected == nil && len(wildcards) > 0 {
		selected = wildcards[0]
	}
	if selected == nil {
		return p2pstreamv1.PublicSiteTlsCoverage_PUBLIC_SITE_TLS_COVERAGE_MISSING, "no enabled certificate mapping covers hostname"
	}
	pair, err := tls.LoadX509KeyPair(selected.CertPath, selected.KeyPath)
	if err != nil || len(pair.Certificate) == 0 {
		return p2pstreamv1.PublicSiteTlsCoverage_PUBLIC_SITE_TLS_COVERAGE_INVALID, "selected certificate material is unavailable"
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || time.Now().Before(leaf.NotBefore) || time.Now().After(leaf.NotAfter) {
		return p2pstreamv1.PublicSiteTlsCoverage_PUBLIC_SITE_TLS_COVERAGE_INVALID, "selected certificate is outside its validity period"
	}
	if strings.HasPrefix(hostname, "*.") {
		for _, dnsName := range leaf.DNSNames {
			normalized, normErr := normalizePublicSiteHostnamePattern(dnsName)
			if normErr == nil && normalized == hostname {
				return p2pstreamv1.PublicSiteTlsCoverage_PUBLIC_SITE_TLS_COVERAGE_COVERED, "certificate SAN covers wildcard"
			}
		}
	} else if leaf.VerifyHostname(hostname) == nil {
		return p2pstreamv1.PublicSiteTlsCoverage_PUBLIC_SITE_TLS_COVERAGE_COVERED, "certificate SAN covers hostname"
	}
	return p2pstreamv1.PublicSiteTlsCoverage_PUBLIC_SITE_TLS_COVERAGE_INVALID, "selected certificate SAN does not cover hostname"
}

// strictPublicSiteHostMatches uses X.509-style one-label wildcard semantics.
// It is deliberately separate from the legacy route and policy matcher.
func strictPublicSiteHostMatches(hostname, pattern string) bool {
	hostname = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(hostname)), ".")
	pattern = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(pattern)), ".")
	if !strings.HasPrefix(pattern, "*.") {
		return hostname == pattern
	}
	suffix := strings.TrimPrefix(pattern, "*.")
	if !strings.HasSuffix(hostname, "."+suffix) {
		return false
	}
	prefix := strings.TrimSuffix(hostname, "."+suffix)
	return prefix != "" && !strings.Contains(prefix, ".")
}
