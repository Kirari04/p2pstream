package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
)

const (
	CloudflareIPRangesURL = "https://api.cloudflare.com/client/v4/ips"
	BunnyIPv4RangesURL    = "https://bunnycdn.com/api/system/edgeserverlist/plain"
	BunnyIPv6RangesURL    = "https://bunnycdn.com/api/system/edgeserverlist/IPv6/plain"
	CloudFrontIPRangesURL = "https://ip-ranges.amazonaws.com/ip-ranges.json"

	MaxTrustedProxyProviderDocumentBytes = 16 << 20
	MaxTrustedProxyProviderPrefixes      = 16_384
)

// BuiltinTrustedProxySource supplies immutable provider header behavior for a
// freshly fetched prefix snapshot. Callers still decide whether to enable it.
func BuiltinTrustedProxySource(provider TrustedProxyProvider, prefixes []netip.Prefix) (TrustedProxySource, error) {
	prefixes, err := validateProviderPrefixes(prefixes)
	if err != nil {
		return TrustedProxySource{}, err
	}
	source := TrustedProxySource{
		Name:     string(provider),
		Provider: provider,
		Enabled:  true,
		Prefixes: prefixes,
	}
	switch provider {
	case TrustedProxyProviderCloudflare:
		source.HeaderName = "CF-Connecting-IP"
		source.HeaderMode = TrustedProxyHeaderSingleIP
	case TrustedProxyProviderBunny:
		source.HeaderName = "X-Real-IP"
		source.HeaderMode = TrustedProxyHeaderSingleIP
	case TrustedProxyProviderCloudFront:
		source.HeaderName = "X-Forwarded-For"
		source.HeaderMode = TrustedProxyHeaderTrustedChain
	default:
		return TrustedProxySource{}, fmt.Errorf("unsupported built-in trusted proxy provider %q", provider)
	}
	return source, nil
}

// FetchTrustedProxyProviderPrefixes fetches only hard-coded official provider
// URLs. A custom source cannot supply a download URL through this API.
func FetchTrustedProxyProviderPrefixes(ctx context.Context, client HTTPClient, provider TrustedProxyProvider) ([]netip.Prefix, error) {
	if client == nil {
		client = defaultProviderRangeHTTPClient()
	}
	switch provider {
	case TrustedProxyProviderCloudflare:
		document, err := fetchTrustedProxyProviderDocument(ctx, client, CloudflareIPRangesURL)
		if err != nil {
			return nil, fmt.Errorf("fetch Cloudflare IP ranges: %w", err)
		}
		prefixes, err := ParseCloudflareIPRanges(bytes.NewReader(document))
		if err != nil {
			return nil, fmt.Errorf("parse Cloudflare IP ranges: %w", err)
		}
		return prefixes, nil
	case TrustedProxyProviderBunny:
		ipv4Document, err := fetchTrustedProxyProviderDocument(ctx, client, BunnyIPv4RangesURL)
		if err != nil {
			return nil, fmt.Errorf("fetch Bunny IPv4 ranges: %w", err)
		}
		ipv6Document, err := fetchTrustedProxyProviderDocument(ctx, client, BunnyIPv6RangesURL)
		if err != nil {
			return nil, fmt.Errorf("fetch Bunny IPv6 ranges: %w", err)
		}
		ipv4, err := parseBunnyIPRanges(bytes.NewReader(ipv4Document), 32)
		if err != nil {
			return nil, fmt.Errorf("parse Bunny IPv4 ranges: %w", err)
		}
		ipv6, err := parseBunnyIPRanges(bytes.NewReader(ipv6Document), 128)
		if err != nil {
			return nil, fmt.Errorf("parse Bunny IPv6 ranges: %w", err)
		}
		return validateProviderPrefixes(append(ipv4, ipv6...))
	case TrustedProxyProviderCloudFront:
		document, err := fetchTrustedProxyProviderDocument(ctx, client, CloudFrontIPRangesURL)
		if err != nil {
			return nil, fmt.Errorf("fetch CloudFront IP ranges: %w", err)
		}
		prefixes, err := ParseCloudFrontIPRanges(bytes.NewReader(document))
		if err != nil {
			return nil, fmt.Errorf("parse CloudFront IP ranges: %w", err)
		}
		return prefixes, nil
	default:
		return nil, fmt.Errorf("trusted proxy provider %q has no managed range source", provider)
	}
}

func fetchTrustedProxyProviderDocument(ctx context.Context, client HTTPClient, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json, text/plain;q=0.9")
	req.Header.Set("User-Agent", "p2pstream-geoip-updater")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, errors.New("HTTP client returned a nil response")
	}
	if resp.Body == nil {
		return nil, errors.New("HTTP response has no body")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("unexpected HTTP status %d", resp.StatusCode)
	}
	if resp.ContentLength > MaxTrustedProxyProviderDocumentBytes {
		return nil, errors.New("provider response exceeds size limit")
	}
	return readBounded(resp.Body, MaxTrustedProxyProviderDocumentBytes)
}

// ParseCloudflareIPRanges parses the public v4 /ips response.
func ParseCloudflareIPRanges(reader io.Reader) ([]netip.Prefix, error) {
	document, err := readBounded(reader, MaxTrustedProxyProviderDocumentBytes)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Success bool `json:"success"`
		Result  struct {
			IPv4CIDRs []string `json:"ipv4_cidrs"`
			IPv6CIDRs []string `json:"ipv6_cidrs"`
		} `json:"result"`
	}
	if err := decodeSingleJSON(document, &envelope); err != nil {
		return nil, err
	}
	if !envelope.Success {
		return nil, errors.New("Cloudflare IP range response was not successful")
	}
	values := append(append([]string(nil), envelope.Result.IPv4CIDRs...), envelope.Result.IPv6CIDRs...)
	return parseProviderPrefixStrings(values)
}

// ParseBunnyIPRanges parses one of Bunny's official plain IP-list responses.
// Both bare addresses and CIDRs are accepted because the endpoint has emitted
// both formats over its lifetime.
func ParseBunnyIPRanges(reader io.Reader) ([]netip.Prefix, error) {
	return parseBunnyIPRanges(reader, 0 /* family is not constrained */)
}

func parseBunnyIPRanges(reader io.Reader, expectedBitLen int) ([]netip.Prefix, error) {
	document, err := readBounded(reader, MaxTrustedProxyProviderDocumentBytes)
	if err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(bytes.NewReader(document))
	scanner.Buffer(make([]byte, 256), 1024)
	values := make([]netip.Prefix, 0)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		prefix, err := parseProviderAddressOrPrefix(line)
		if err != nil {
			return nil, err
		}
		if expectedBitLen != 0 && prefix.Addr().BitLen() != expectedBitLen {
			return nil, errors.New("Bunny response contained an address of the wrong family")
		}
		values = append(values, prefix)
		if len(values) > MaxTrustedProxyProviderPrefixes {
			return nil, errors.New("provider response contains too many prefixes")
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return validateProviderPrefixes(values)
}

// ParseCloudFrontIPRanges selects only the origin-facing CloudFront addresses
// that can be the immediate network peer of a p2pstream listener. Viewer-edge
// CLOUDFRONT ranges are a distinct AWS service and must not be trusted here.
func ParseCloudFrontIPRanges(reader io.Reader) ([]netip.Prefix, error) {
	document, err := readBounded(reader, MaxTrustedProxyProviderDocumentBytes)
	if err != nil {
		return nil, err
	}
	var ranges struct {
		Prefixes []struct {
			IPPrefix string `json:"ip_prefix"`
			Service  string `json:"service"`
		} `json:"prefixes"`
		IPv6Prefixes []struct {
			IPv6Prefix string `json:"ipv6_prefix"`
			Service    string `json:"service"`
		} `json:"ipv6_prefixes"`
	}
	if err := decodeSingleJSON(document, &ranges); err != nil {
		return nil, err
	}
	values := make([]string, 0)
	for _, prefix := range ranges.Prefixes {
		if prefix.Service == "CLOUDFRONT_ORIGIN_FACING" {
			values = append(values, prefix.IPPrefix)
		}
	}
	for _, prefix := range ranges.IPv6Prefixes {
		if prefix.Service == "CLOUDFRONT_ORIGIN_FACING" {
			values = append(values, prefix.IPv6Prefix)
		}
	}
	return parseProviderPrefixStrings(values)
}

func parseProviderPrefixStrings(values []string) ([]netip.Prefix, error) {
	if len(values) > MaxTrustedProxyProviderPrefixes {
		return nil, errors.New("provider response contains too many prefixes")
	}
	prefixes := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(value))
		if err != nil || prefix.Addr().Zone() != "" {
			return nil, errors.New("provider response contains an invalid prefix")
		}
		prefixes = append(prefixes, prefix)
	}
	return validateProviderPrefixes(prefixes)
}

func parseProviderAddressOrPrefix(value string) (netip.Prefix, error) {
	if prefix, err := netip.ParsePrefix(value); err == nil && prefix.Addr().Zone() == "" {
		return prefix, nil
	}
	addr, err := netip.ParseAddr(value)
	if err != nil || addr.Zone() != "" {
		return netip.Prefix{}, errors.New("provider response contains an invalid address or prefix")
	}
	addr = addr.Unmap()
	return netip.PrefixFrom(addr, addr.BitLen()), nil
}

func validateProviderPrefixes(prefixes []netip.Prefix) ([]netip.Prefix, error) {
	if len(prefixes) == 0 {
		return nil, errors.New("provider response contains no prefixes")
	}
	if len(prefixes) > MaxTrustedProxyProviderPrefixes {
		return nil, errors.New("provider response contains too many prefixes")
	}
	normalized, err := normalizeTrustedProxyPrefixes(prefixes)
	if err != nil {
		return nil, err
	}
	for _, prefix := range normalized {
		if prefix.Bits() == 0 {
			return nil, errors.New("provider response contains a default route")
		}
		minimumBits := 16
		if prefix.Addr().Is4() {
			minimumBits = 8
		}
		if prefix.Bits() < minimumBits || !isPublicGeoIPAddress(prefix.Addr()) {
			return nil, errors.New("provider response contains an implausible public range")
		}
	}
	return normalized, nil
}

func decodeSingleJSON(document []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(document))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("provider response contains multiple JSON values")
		}
		return err
	}
	return nil
}

func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	if reader == nil {
		return nil, errors.New("reader is nil")
	}
	limited := io.LimitReader(reader, limit+1)
	document, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(document)) > limit {
		return nil, fmt.Errorf("document exceeds %s byte limit", strconv.FormatInt(limit, 10))
	}
	return document, nil
}
