package server

import (
	htmltemplate "html/template"
	"strings"
	"testing"

	"connectrpc.com/connect"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
)

func TestPublicResponseTemplateValidationRejectsInvalidWafPlaceholders(t *testing.T) {
	cases := []struct {
		name string
		kind p2pstreamv1.PublicResponseTemplateKind
		body string
	}{
		{
			name: "missing-captcha",
			kind: p2pstreamv1.PublicResponseTemplateKind_PUBLIC_RESPONSE_TEMPLATE_KIND_WAF_CAPTCHA_PAGE,
			body: `<html>{{ .host }}</html>`,
		},
		{
			name: "unknown-placeholder",
			kind: p2pstreamv1.PublicResponseTemplateKind_PUBLIC_RESPONSE_TEMPLATE_KIND_WAF_CAPTCHA_PAGE,
			body: `<html>{{ .captcha_element_html }} {{ .not_allowed }}</html>`,
		},
		{
			name: "missing-waiting-room",
			kind: p2pstreamv1.PublicResponseTemplateKind_PUBLIC_RESPONSE_TEMPLATE_KIND_WAF_WAITING_ROOM_PAGE,
			body: `<html>{{ .queue_position }}</html>`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validatePublicResponseTemplateInput(tc.name, tc.kind, "", "", tc.body)
			if connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Fatalf("expected invalid template error, got %v", err)
			}
		})
	}
}

func TestPublicResponseTemplateValidationAcceptsRequiredWafPlaceholders(t *testing.T) {
	captcha, err := validatePublicResponseTemplateInput(
		"captcha-template",
		p2pstreamv1.PublicResponseTemplateKind_PUBLIC_RESPONSE_TEMPLATE_KIND_WAF_CAPTCHA_PAGE,
		"",
		"",
		`<html>{{ .page_title }} {{ .captcha_element_html }}</html>`,
	)
	if err != nil {
		t.Fatalf("captcha template rejected: %v", err)
	}
	if captcha.Kind != publicResponseTemplateKindWafCaptchaPage {
		t.Fatalf("captcha kind = %q", captcha.Kind)
	}

	waiting, err := validatePublicResponseTemplateInput(
		"waiting-template",
		p2pstreamv1.PublicResponseTemplateKind_PUBLIC_RESPONSE_TEMPLATE_KIND_WAF_WAITING_ROOM_PAGE,
		"",
		"",
		`<html>{{ .queue_position }} {{ .retry_after_seconds }} {{ .status_url }}</html>`,
	)
	if err != nil {
		t.Fatalf("waiting-room template rejected: %v", err)
	}
	if waiting.Kind != publicResponseTemplateKindWafWaitingRoomPage {
		t.Fatalf("waiting kind = %q", waiting.Kind)
	}
}

func TestPublicWafTemplateRenderEscapesNormalValuesAndTrustsCaptchaElement(t *testing.T) {
	var out strings.Builder
	err := renderPublicWafHTMLTemplate(&out, `<main>{{ .host }} {{ .captcha_element_html }}</main>`, map[string]any{
		"host":                 `bad.example"><script>alert(1)</script>`,
		"captcha_element_html": htmltemplate.HTML(`<form><div class="cf-turnstile" data-sitekey="preview"></div></form>`),
	})
	if err != nil {
		t.Fatalf("render template: %v", err)
	}
	body := out.String()
	if !strings.Contains(body, `bad.example&#34;&gt;&lt;script&gt;alert(1)&lt;/script&gt;`) {
		t.Fatalf("normal placeholder was not escaped\n%s", body)
	}
	if !strings.Contains(body, `<form><div class="cf-turnstile"`) {
		t.Fatalf("captcha element was escaped or missing\n%s", body)
	}
}
