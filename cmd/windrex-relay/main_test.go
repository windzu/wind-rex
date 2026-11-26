package main

import (
	"strings"
	"testing"
)

func TestPortSuffix(t *testing.T) {
	cases := []struct {
		listen string
		want   string
	}{
		{":7443", ":7443"},
		{"0.0.0.0:8443", ":8443"},
		{":443", ""},
		{"invalid", ""},
	}
	for _, tc := range cases {
		if got := portSuffix(tc.listen); got != tc.want {
			t.Errorf("portSuffix(%q) = %q, want %q", tc.listen, got, tc.want)
		}
	}
}

func TestFormatStartupInfo(t *testing.T) {
	withSecret := formatStartupInfo("https", "0.0.0.0:7443", ":7443", "abc123pin", false, true)
	if !strings.Contains(withSecret, "host discovery: enabled at GET /api/hosts") {
		t.Fatalf("missing enabled discovery note: %q", withSecret)
	}
	if !strings.Contains(withSecret, "--secret <secret>") {
		t.Fatalf("missing secret placeholder in connect hint: %q", withSecret)
	}

	withoutSecret := formatStartupInfo("https", "0.0.0.0:7443", ":7443", "abc123pin", false, false)
	if !strings.Contains(withoutSecret, "host discovery: disabled") {
		t.Fatalf("missing disabled discovery note: %q", withoutSecret)
	}
	if strings.Contains(withoutSecret, "--secret") {
		t.Fatalf("unexpected --secret hint when secret is unset: %q", withoutSecret)
	}
}
