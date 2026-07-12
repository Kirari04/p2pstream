package server

import (
	"net/http"
	"net/netip"
	"strings"
)

const (
	publicWafGeoModeDisabled        = "disabled"
	publicWafGeoModeSelected        = "selected_countries"
	publicWafGeoModeOutsideSelected = "outside_selected_countries"

	publicWafGeoUnknownApplyRule  = "apply_rule"
	publicWafGeoUnknownBypassRule = "bypass_rule"
)

// publicWafGeoRestrictionConfig narrows a WAF rule after its ordinary request
// match succeeds. CountryCodes are normalized to uppercase at mutation/load
// boundaries; the evaluator still normalizes its lookup input defensively.
type publicWafGeoRestrictionConfig struct {
	Mode            string
	CountryCodes    []string
	UnknownBehavior string
}

func (g publicWafGeoRestrictionConfig) enabled() bool {
	return g.Mode == publicWafGeoModeSelected || g.Mode == publicWafGeoModeOutsideSelected
}

// matches reports whether the rule should apply to this geography. An unknown
// result is deliberately policy-controlled: administrators can fail closed by
// applying the rule or explicitly fail open by bypassing the geo restriction.
func (g publicWafGeoRestrictionConfig) matches(countryCode string, known bool) bool {
	if !g.enabled() {
		return true
	}
	code := strings.ToUpper(strings.TrimSpace(countryCode))
	if !known || code == "" {
		return g.UnknownBehavior != publicWafGeoUnknownBypassRule
	}
	selected := false
	for _, candidate := range g.CountryCodes {
		if strings.EqualFold(strings.TrimSpace(candidate), code) {
			selected = true
			break
		}
	}
	if g.Mode == publicWafGeoModeOutsideSelected {
		return !selected
	}
	return selected
}

type publicGeoCountryLookup interface {
	LookupCountry(netip.Addr) (string, bool, error)
}

func evaluatePublicWafGeoRestriction(app *App, r *http.Request, restriction publicWafGeoRestrictionConfig) (matches bool, countryCode string, known bool) {
	if !restriction.enabled() {
		return true, "", false
	}
	addr, resolved := publicRequestResolvedAddr(r)
	if !resolved || app == nil {
		return restriction.matches("", false), "", false
	}
	lookup, ok := app.GeoConfigRefresher.(publicGeoCountryLookup)
	if !ok || lookup == nil {
		return restriction.matches("", false), "", false
	}
	code, known, err := lookup.LookupCountry(addr)
	if err != nil {
		return restriction.matches("", false), "", false
	}
	code = strings.ToUpper(strings.TrimSpace(code))
	known = known && code != ""
	return restriction.matches(code, known), code, known
}

func publicRequestResolvedAddr(r *http.Request) (netip.Addr, bool) {
	if r == nil {
		return netip.Addr{}, false
	}
	if _, attached := ClientIdentityFromContext(r.Context()); attached {
		return ClientIdentityResolved(r.Context())
	}
	return peerAddrFromRequest(r)
}

func publicWafDecisionWithGeo(decision publicWafDecision, restriction publicWafGeoRestrictionConfig, countryCode string, known bool) publicWafDecision {
	decision.GeoCountryCode = countryCode
	decision.GeoCountryKnown = known
	decision.GeoRestrictionApplied = restriction.enabled()
	return decision
}
