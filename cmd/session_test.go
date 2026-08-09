package cmd

import (
	"net/url"
	"strings"
	"testing"
)

func TestSessionCredentialsStayInURLFragment(t *testing.T) {
	shareURL, err := appendSessionCredentials("https://example.trycloudflare.com", "join-secret", "e2e-secret")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(shareURL)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.RawQuery != "" {
		t.Fatalf("credentials leaked into URL query: %q", parsed.RawQuery)
	}
	if !strings.Contains(parsed.Fragment, "key=") || !strings.Contains(parsed.Fragment, "token=") {
		t.Fatalf("credentials are not in fragment: %q", parsed.Fragment)
	}

	wsURL, key, token, err := normalizeSessionWSURL(shareURL)
	if err != nil {
		t.Fatal(err)
	}
	if key != "e2e-secret" || token != "join-secret" {
		t.Fatalf("parsed credentials = (%q, %q)", key, token)
	}
	if strings.Contains(wsURL, "e2e-secret") || strings.Contains(wsURL, "join-secret") || strings.Contains(wsURL, "#") {
		t.Fatalf("credentials leaked into websocket URL: %q", wsURL)
	}
}

func TestNormalizeSessionURLRequiresAccessToken(t *testing.T) {
	if _, _, _, err := normalizeSessionWSURL("https://example.test#key=only-a-key"); err == nil {
		t.Fatal("session URL without access token was accepted")
	}
}
