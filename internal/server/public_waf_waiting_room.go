package server

import (
	"fmt"
	"html"
	"net/http"
	"strconv"
	"time"
)

type publicWaitingRoomRuntime struct {
	queue           []string
	queued          map[string]time.Time
	admitted        map[string]time.Time
	admissionTokens float64
	lastRefillAt    time.Time
}

func newPublicWaitingRoomRuntime() *publicWaitingRoomRuntime {
	return &publicWaitingRoomRuntime{
		queued:   make(map[string]time.Time),
		admitted: make(map[string]time.Time),
	}
}

func (rt *publicWaitingRoomRuntime) evaluateLocked(w *publicWAF, rule publicWafRuleConfig, listener publicListenerConfig, r *http.Request, now time.Time, automaticActive bool) (publicWafDecision, bool) {
	if rt == nil {
		rt = newPublicWaitingRoomRuntime()
	}
	if w.validRuleCookieLocked(r, rule.ID, publicWafAdmissionCookieKind, now) {
		return publicWafDecision{}, true
	}
	sessionID := ""
	if payload, ok := w.queueCookiePayloadLocked(r, rule.ID, now); ok {
		sessionID = payload.SessionID
	}
	if sessionID == "" {
		sessionID = randomWAFSessionID()
	}
	queueTimeout := time.Duration(rule.WaitingRoom.QueueTimeoutMillis) * time.Millisecond
	if queueTimeout <= 0 {
		queueTimeout = defaultWafWaitingRoomQueueTimeout
	}
	rt.pruneLocked(now, queueTimeout)
	if _, ok := rt.queued[sessionID]; !ok {
		rt.queued[sessionID] = now
		rt.queue = append(rt.queue, sessionID)
	}
	position := rt.positionLocked(sessionID)
	queueCookie := w.signedRuleCookie(rule.ID, publicWafQueueCookieKind, sessionID, queueTimeout, now)
	if rt.canAdmitLocked(rule, now, sessionID) {
		admissionTTL := time.Duration(rule.WaitingRoom.AdmissionSessionTTLMillis) * time.Millisecond
		if admissionTTL <= 0 {
			admissionTTL = defaultWafWaitingRoomAdmissionTTL
		}
		delete(rt.queued, sessionID)
		rt.removeFromQueueLocked(sessionID)
		rt.admitted[sessionID] = now.Add(admissionTTL)
		return publicWafDecision{
			Rule:             rule,
			Listener:         listener,
			Action:           publicWafActionWaitingRoom,
			StatusCode:       http.StatusSeeOther,
			RedirectLocation: sanitizeWAFReturnTo(r.URL.RequestURI()),
			Cookies:          []*http.Cookie{queueCookie, w.signedRuleCookie(rule.ID, publicWafAdmissionCookieKind, sessionID, admissionTTL, now)},
			AutomaticActive:  automaticActive,
			ChallengeKind:    publicWafActionWaitingRoom,
			QueuePosition:    position,
		}, false
	}
	retryAfter := time.Duration(rule.WaitingRoom.QueuePollIntervalMillis) * time.Millisecond
	if retryAfter <= 0 {
		retryAfter = defaultWafWaitingRoomPollInterval
	}
	return publicWafDecision{
		Rule:            rule,
		Listener:        listener,
		Action:          publicWafActionWaitingRoom,
		StatusCode:      http.StatusServiceUnavailable,
		ErrorKind:       "waf_waiting_room",
		Cookies:         []*http.Cookie{queueCookie},
		RetryAfter:      retryAfter,
		AutomaticActive: automaticActive,
		ChallengeKind:   publicWafActionWaitingRoom,
		QueuePosition:   position,
	}, false
}

func (rt *publicWaitingRoomRuntime) pruneLocked(now time.Time, queueTimeout time.Duration) {
	for sessionID, expiresAt := range rt.admitted {
		if !expiresAt.After(now) {
			delete(rt.admitted, sessionID)
		}
	}
	if queueTimeout <= 0 {
		queueTimeout = defaultWafWaitingRoomQueueTimeout
	}
	keep := rt.queue[:0]
	for _, sessionID := range rt.queue {
		enqueuedAt, ok := rt.queued[sessionID]
		if !ok {
			continue
		}
		if now.Sub(enqueuedAt) > queueTimeout {
			delete(rt.queued, sessionID)
			continue
		}
		keep = append(keep, sessionID)
	}
	rt.queue = keep
}

func (rt *publicWaitingRoomRuntime) canAdmitLocked(rule publicWafRuleConfig, now time.Time, sessionID string) bool {
	if len(rt.queue) == 0 || rt.queue[0] != sessionID {
		return false
	}
	maxAdmitted := rule.WaitingRoom.MaxAdmittedSessions
	if maxAdmitted <= 0 {
		maxAdmitted = defaultWafWaitingRoomMaxAdmitted
	}
	if int64(len(rt.admitted)) >= maxAdmitted {
		return false
	}
	rate := rule.WaitingRoom.AdmissionRatePerSecond
	if rate <= 0 {
		rate = defaultWafWaitingRoomAdmissionRate
	}
	if rt.lastRefillAt.IsZero() {
		rt.lastRefillAt = now
		rt.admissionTokens = float64(rate)
	}
	elapsed := now.Sub(rt.lastRefillAt).Seconds()
	if elapsed > 0 {
		rt.admissionTokens += elapsed * float64(rate)
		if rt.admissionTokens > float64(rate) {
			rt.admissionTokens = float64(rate)
		}
		rt.lastRefillAt = now
	}
	if rt.admissionTokens < 1 {
		return false
	}
	rt.admissionTokens--
	return true
}

func (rt *publicWaitingRoomRuntime) positionLocked(sessionID string) int64 {
	for idx, queuedID := range rt.queue {
		if queuedID == sessionID {
			return int64(idx + 1)
		}
	}
	return int64(len(rt.queue) + 1)
}

func (rt *publicWaitingRoomRuntime) removeFromQueueLocked(sessionID string) {
	for idx, queuedID := range rt.queue {
		if queuedID == sessionID {
			copy(rt.queue[idx:], rt.queue[idx+1:])
			rt.queue = rt.queue[:len(rt.queue)-1]
			return
		}
	}
}

func writeWaitingRoomPage(w http.ResponseWriter, decision publicWafDecision) {
	status := decision.StatusCode
	if status == 0 {
		status = http.StatusServiceUnavailable
	}
	title := decision.Rule.WaitingRoom.PageTitle
	if title == "" {
		title = defaultWafWaitingRoomPageTitle
	}
	body := decision.Rule.WaitingRoom.PageBody
	if body == "" {
		body = defaultWafWaitingRoomPageBody
	}
	pollSeconds := maxInt(1, int(decision.RetryAfter.Seconds()))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	primaryHTML := fmt.Sprintf(`<div class="cf-queue-grid">
        <div class="cf-stat">
          <span>Queue position</span>
          <strong>%s</strong>
        </div>
        <div class="cf-stat">
          <span>Next check</span>
          <strong>%ss</strong>
        </div>
      </div>`,
		html.EscapeString(strconv.FormatInt(decision.QueuePosition, 10)),
		html.EscapeString(strconv.Itoa(pollSeconds)),
	)
	writePublicWafInterstitialPage(w, publicWafPageModel{
		Title:          title,
		Heading:        title,
		Lead:           body,
		ReferenceID:    publicWafReferenceID(decision.Rule.ID),
		FooterLabel:    "Waiting room by p2pstream",
		RefreshSeconds: pollSeconds,
		Diagnostics: []publicWafPageDiagnostic{
			{Label: "Browser", Status: "Connected", Tone: "ok"},
			{Label: "p2pstream", Status: "Waiting room", Tone: "warn"},
			{Label: "Destination", Status: "Protected", Tone: "muted"},
		},
		PrimaryHTML:   primaryHTML,
		SecondaryHTML: `<p>Keep this page open. You will continue automatically when capacity is available. Request bodies are not replayed after admission.</p>`,
	})
}

func (a *App) servePublicWAFWaitingRoomStatus(w http.ResponseWriter, r *http.Request, listenerID int64) publicWafDecision {
	ruleID, _ := strconv.ParseInt(r.URL.Query().Get("rule_id"), 10, 64)
	a.proxyMu.Lock()
	snap := a.publicSnapshot
	a.proxyMu.Unlock()
	if snap == nil || a.PublicWAF == nil || ruleID <= 0 {
		http.NotFound(w, r)
		return publicWafDecision{Action: publicWafActionWaitingRoom, StatusCode: http.StatusNotFound, ErrorKind: "waf_waiting_room_not_found"}
	}
	var rule publicWafRuleConfig
	for _, candidate := range snap.WafRules {
		if candidate.ID == ruleID && candidate.Action == publicWafActionWaitingRoom {
			rule = candidate
			break
		}
	}
	if rule.ID == 0 {
		http.NotFound(w, r)
		return publicWafDecision{Action: publicWafActionWaitingRoom, StatusCode: http.StatusNotFound, ErrorKind: "waf_waiting_room_not_found"}
	}
	listener := publicListenerConfig{ID: listenerID}
	if configured, ok := snap.Listeners[listenerID]; ok {
		listener = configured
	}
	now := time.Now()
	a.PublicWAF.mu.Lock()
	defer a.PublicWAF.mu.Unlock()
	if a.PublicWAF.validRuleCookieLocked(r, rule.ID, publicWafAdmissionCookieKind, now) {
		http.Redirect(w, r, sanitizeWAFReturnTo(r.URL.Query().Get("return_to")), http.StatusSeeOther)
		return publicWafDecision{Rule: rule, Listener: listener, Action: publicWafActionWaitingRoom, StatusCode: http.StatusSeeOther, ChallengeKind: publicWafActionWaitingRoom}
	}
	runtime := a.PublicWAF.runtimeLocked(rule)
	sessionID := ""
	if payload, ok := a.PublicWAF.queueCookiePayloadLocked(r, rule.ID, now); ok {
		sessionID = payload.SessionID
	}
	position := int64(0)
	if sessionID != "" {
		position = runtime.waitingRoom.positionLocked(sessionID)
	}
	decision := publicWafDecision{
		Rule:          rule,
		Listener:      listener,
		Action:        publicWafActionWaitingRoom,
		StatusCode:    http.StatusServiceUnavailable,
		ErrorKind:     "waf_waiting_room",
		RetryAfter:    time.Duration(rule.WaitingRoom.QueuePollIntervalMillis) * time.Millisecond,
		ChallengeKind: publicWafActionWaitingRoom,
		QueuePosition: position,
	}
	writeWaitingRoomPage(w, decision)
	return decision
}

func maxInt(a int, b int) int {
	if a > b {
		return a
	}
	return b
}
