package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
)

const maxPublicWafGeoCountryCodes = 256

func normalizePublicWafGeoMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case publicWafGeoModeSelected:
		return publicWafGeoModeSelected
	case publicWafGeoModeOutsideSelected:
		return publicWafGeoModeOutsideSelected
	default:
		return publicWafGeoModeDisabled
	}
}

func normalizePublicWafGeoUnknownBehavior(behavior string) string {
	if strings.EqualFold(strings.TrimSpace(behavior), publicWafGeoUnknownBypassRule) {
		return publicWafGeoUnknownBypassRule
	}
	return publicWafGeoUnknownApplyRule
}

func publicWafGeoModeFromProto(mode p2pstreamv1.PublicWafGeoRestrictionMode) (string, error) {
	switch mode {
	case p2pstreamv1.PublicWafGeoRestrictionMode_PUBLIC_WAF_GEO_RESTRICTION_MODE_UNSPECIFIED,
		p2pstreamv1.PublicWafGeoRestrictionMode_PUBLIC_WAF_GEO_RESTRICTION_MODE_DISABLED:
		return publicWafGeoModeDisabled, nil
	case p2pstreamv1.PublicWafGeoRestrictionMode_PUBLIC_WAF_GEO_RESTRICTION_MODE_SELECTED_COUNTRIES:
		return publicWafGeoModeSelected, nil
	case p2pstreamv1.PublicWafGeoRestrictionMode_PUBLIC_WAF_GEO_RESTRICTION_MODE_OUTSIDE_SELECTED_COUNTRIES:
		return publicWafGeoModeOutsideSelected, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("WAF geo restriction mode is invalid"))
	}
}

func publicWafGeoUnknownBehaviorFromProto(behavior p2pstreamv1.PublicWafGeoUnknownBehavior) (string, error) {
	switch behavior {
	case p2pstreamv1.PublicWafGeoUnknownBehavior_PUBLIC_WAF_GEO_UNKNOWN_BEHAVIOR_UNSPECIFIED,
		p2pstreamv1.PublicWafGeoUnknownBehavior_PUBLIC_WAF_GEO_UNKNOWN_BEHAVIOR_APPLY_RULE:
		return publicWafGeoUnknownApplyRule, nil
	case p2pstreamv1.PublicWafGeoUnknownBehavior_PUBLIC_WAF_GEO_UNKNOWN_BEHAVIOR_BYPASS_RULE:
		return publicWafGeoUnknownBypassRule, nil
	default:
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("WAF unknown-country behavior is invalid"))
	}
}

func validatePublicWafGeoRestriction(input *p2pstreamv1.PublicWafGeoRestriction) (publicWafGeoRestrictionConfig, error) {
	if input == nil {
		return publicWafGeoRestrictionConfig{Mode: publicWafGeoModeDisabled, UnknownBehavior: publicWafGeoUnknownApplyRule}, nil
	}
	if len(input.CountryCodes) > maxPublicWafGeoCountryCodes {
		return publicWafGeoRestrictionConfig{}, connect.NewError(connect.CodeInvalidArgument, errors.New("WAF geo restriction has too many country codes"))
	}
	for _, rawCode := range input.CountryCodes {
		if len(rawCode) > 16 {
			return publicWafGeoRestrictionConfig{}, connect.NewError(connect.CodeInvalidArgument, errors.New("WAF geo country code is too long"))
		}
	}
	mode, err := publicWafGeoModeFromProto(input.Mode)
	if err != nil {
		return publicWafGeoRestrictionConfig{}, err
	}
	unknownBehavior, err := publicWafGeoUnknownBehaviorFromProto(input.UnknownBehavior)
	if err != nil {
		return publicWafGeoRestrictionConfig{}, err
	}
	if mode == publicWafGeoModeDisabled {
		return publicWafGeoRestrictionConfig{Mode: mode, CountryCodes: []string{}, UnknownBehavior: unknownBehavior}, nil
	}
	countryCodes, err := normalizePublicWafGeoCountryCodes(input.CountryCodes)
	if err != nil {
		return publicWafGeoRestrictionConfig{}, err
	}
	if len(countryCodes) == 0 {
		return publicWafGeoRestrictionConfig{}, connect.NewError(connect.CodeInvalidArgument, errors.New("enabled WAF geo restrictions require at least one country code"))
	}
	return publicWafGeoRestrictionConfig{Mode: mode, CountryCodes: countryCodes, UnknownBehavior: unknownBehavior}, nil
}

func normalizePublicWafGeoCountryCodes(input []string) ([]string, error) {
	if len(input) > maxPublicWafGeoCountryCodes {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("WAF geo restriction has too many country codes"))
	}
	seen := make(map[string]struct{}, len(input))
	result := make([]string, 0, len(input))
	for _, rawCode := range input {
		if len(rawCode) > 16 {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("WAF geo country code is too long"))
		}
		code := strings.ToUpper(strings.TrimSpace(rawCode))
		if len(code) != 2 || code == "XX" || code[0] < 'A' || code[0] > 'Z' || code[1] < 'A' || code[1] > 'Z' {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("WAF geo country code %q must contain exactly two ASCII letters and cannot be XX", rawCode))
		}
		if _, exists := seen[code]; exists {
			continue
		}
		seen[code] = struct{}{}
		result = append(result, code)
	}
	sort.Strings(result)
	return result, nil
}

func decodeStoredPublicWafGeoRestriction(mode, rawCountryCodes, unknownBehavior string) (publicWafGeoRestrictionConfig, error) {
	normalizedMode := strings.ToLower(strings.TrimSpace(mode))
	if normalizedMode == "" {
		normalizedMode = publicWafGeoModeDisabled
	}
	switch normalizedMode {
	case publicWafGeoModeDisabled, publicWafGeoModeSelected, publicWafGeoModeOutsideSelected:
	default:
		return publicWafGeoRestrictionConfig{}, fmt.Errorf("stored WAF geo restriction mode %q is invalid", mode)
	}
	normalizedUnknown := strings.ToLower(strings.TrimSpace(unknownBehavior))
	if normalizedUnknown == "" {
		normalizedUnknown = publicWafGeoUnknownApplyRule
	}
	if normalizedUnknown != publicWafGeoUnknownApplyRule && normalizedUnknown != publicWafGeoUnknownBypassRule {
		return publicWafGeoRestrictionConfig{}, fmt.Errorf("stored WAF unknown-country behavior %q is invalid", unknownBehavior)
	}
	if normalizedMode == publicWafGeoModeDisabled {
		return publicWafGeoRestrictionConfig{Mode: normalizedMode, CountryCodes: []string{}, UnknownBehavior: normalizedUnknown}, nil
	}
	var countryCodes []string
	if err := json.Unmarshal([]byte(rawCountryCodes), &countryCodes); err != nil {
		return publicWafGeoRestrictionConfig{}, fmt.Errorf("decode WAF geo country codes: %w", err)
	}
	countryCodes, err := normalizePublicWafGeoCountryCodes(countryCodes)
	if err != nil {
		return publicWafGeoRestrictionConfig{}, err
	}
	if len(countryCodes) == 0 {
		return publicWafGeoRestrictionConfig{}, errors.New("enabled WAF geo restriction has no country codes")
	}
	return publicWafGeoRestrictionConfig{Mode: normalizedMode, CountryCodes: countryCodes, UnknownBehavior: normalizedUnknown}, nil
}

func (a *App) applyPublicWafGeoRestrictionInput(ctx context.Context, ruleEnabled bool, input *p2pstreamv1.PublicWafGeoRestriction, mutation *publicWafRuleMutationInput) error {
	if mutation == nil {
		return connect.NewError(connect.CodeInternal, errors.New("WAF mutation target is required"))
	}
	restriction, err := validatePublicWafGeoRestriction(input)
	if err != nil {
		return err
	}
	if ruleEnabled && restriction.enabled() {
		settings, err := a.ensurePublicGeoIPSettings(ctx)
		if err != nil {
			return err
		}
		ready := settings.DatabaseType != "" && settings.DatabaseBuildAt.Valid && settings.LastUpdateSuccessAt.Valid
		if settings.Enabled == 0 || !ready || !a.publicGeoIPRuntimeMatches(settings) {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("enabled geo-restricted WAF rules require enabled, ready GeoIP settings"))
		}
	}
	countryCodesJSON, _ := json.Marshal(restriction.CountryCodes)
	mutation.GeoMode = restriction.Mode
	mutation.GeoCountryCodesJSON = string(countryCodesJSON)
	mutation.GeoUnknownBehavior = restriction.UnknownBehavior
	return nil
}

func publicWafGeoRestrictionToProto(restriction publicWafGeoRestrictionConfig) *p2pstreamv1.PublicWafGeoRestriction {
	mode := p2pstreamv1.PublicWafGeoRestrictionMode_PUBLIC_WAF_GEO_RESTRICTION_MODE_DISABLED
	switch normalizePublicWafGeoMode(restriction.Mode) {
	case publicWafGeoModeSelected:
		mode = p2pstreamv1.PublicWafGeoRestrictionMode_PUBLIC_WAF_GEO_RESTRICTION_MODE_SELECTED_COUNTRIES
	case publicWafGeoModeOutsideSelected:
		mode = p2pstreamv1.PublicWafGeoRestrictionMode_PUBLIC_WAF_GEO_RESTRICTION_MODE_OUTSIDE_SELECTED_COUNTRIES
	}
	unknownBehavior := p2pstreamv1.PublicWafGeoUnknownBehavior_PUBLIC_WAF_GEO_UNKNOWN_BEHAVIOR_APPLY_RULE
	if normalizePublicWafGeoUnknownBehavior(restriction.UnknownBehavior) == publicWafGeoUnknownBypassRule {
		unknownBehavior = p2pstreamv1.PublicWafGeoUnknownBehavior_PUBLIC_WAF_GEO_UNKNOWN_BEHAVIOR_BYPASS_RULE
	}
	return &p2pstreamv1.PublicWafGeoRestriction{
		Mode:            mode,
		CountryCodes:    append([]string(nil), restriction.CountryCodes...),
		UnknownBehavior: unknownBehavior,
	}
}
