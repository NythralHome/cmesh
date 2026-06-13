package main

import "testing"

func TestLocalWorkerProfiles(t *testing.T) {
	profiles := localWorkerProfiles(5)
	if len(profiles) != 5 {
		t.Fatalf("expected 5 profiles, got %d", len(profiles))
	}

	if profiles[0].name != "local-small" {
		t.Fatalf("expected first profile to be local-small, got %s", profiles[0].name)
	}
	if profiles[3].name == profiles[0].name {
		t.Fatalf("expected repeated profile names to be made unique")
	}
}

func TestLocalHTTPURL(t *testing.T) {
	tests := map[string]string{
		":8080":          "http://127.0.0.1:8080",
		"127.0.0.1:8080": "http://127.0.0.1:8080",
		"0.0.0.0:8080":   "http://0.0.0.0:8080",
	}

	for input, want := range tests {
		if got := localHTTPURL(input); got != want {
			t.Fatalf("localHTTPURL(%q) = %q, want %q", input, got, want)
		}
	}
}
