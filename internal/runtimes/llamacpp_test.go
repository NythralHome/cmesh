package runtimes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSelectLlamaCPPAsset(t *testing.T) {
	release := githubRelease{
		TagName: "b9672",
		Assets: []githubAssetObject{
			{Name: "llama-b9672-bin-macos-arm64.tar.gz", BrowserDownloadURL: "https://example.com/macos-arm64"},
			{Name: "llama-b9672-bin-ubuntu-x64.tar.gz", BrowserDownloadURL: "https://example.com/linux-x64"},
			{Name: "llama-b9672-bin-win-cpu-x64.zip", BrowserDownloadURL: "https://example.com/win-x64"},
		},
	}

	tests := []struct {
		name   string
		goos   string
		goarch string
		want   string
	}{
		{name: "mac apple silicon", goos: "darwin", goarch: "arm64", want: "https://example.com/macos-arm64"},
		{name: "linux amd64", goos: "linux", goarch: "amd64", want: "https://example.com/linux-x64"},
		{name: "windows amd64", goos: "windows", goarch: "amd64", want: "https://example.com/win-x64"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := selectLlamaCPPAsset(release, tt.goos, tt.goarch)
			if err != nil {
				t.Fatal(err)
			}
			if got.URL != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, got.URL)
			}
		})
	}
}

func TestSelectLlamaCPPAssetRejectsUnsupportedPlatform(t *testing.T) {
	_, err := selectLlamaCPPAsset(githubRelease{TagName: "b9672"}, "plan9", "amd64")
	if err == nil {
		t.Fatal("expected unsupported platform error")
	}
}

func TestFallbackLlamaCPPReleaseHasPlatformAssets(t *testing.T) {
	release := fallbackLlamaCPPRelease("b9672")
	if release.TagName != "b9672" {
		t.Fatalf("expected fallback tag b9672, got %q", release.TagName)
	}
	got, err := selectLlamaCPPAsset(release, "darwin", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://github.com/ggml-org/llama.cpp/releases/download/b9672/llama-b9672-bin-macos-arm64.tar.gz"
	if got.URL != want {
		t.Fatalf("expected %q, got %q", want, got.URL)
	}
}

func TestEnsureLlamaCPPMigratesLegacyCache(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	legacyCache := t.TempDir()
	newCache := t.TempDir()
	legacyRuntime := filepath.Join(runtimeDir(legacyCache, "b9672"), "llama-b9672")
	if err := os.MkdirAll(legacyRuntime, 0o755); err != nil {
		t.Fatal(err)
	}
	legacyBinary := filepath.Join(legacyRuntime, llamaBinaryName())
	if err := os.WriteFile(legacyBinary, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CMESH_LLAMA_CPP_LEGACY_CACHE_DIRS", legacyCache)
	t.Setenv("CMESH_LLAMA_CPP_TAG", "test-no-download")

	binary, status, err := EnsureLlamaCPP(t.Context(), newCache)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Ready {
		t.Fatal("expected migrated runtime to be ready")
	}
	if status.Source != "cmesh-runtime-cache-migrated" {
		t.Fatalf("expected migrated source, got %q", status.Source)
	}
	if status.Version != "b9672" {
		t.Fatalf("expected b9672, got %q", status.Version)
	}
	if binary == legacyBinary {
		t.Fatal("expected runtime to be copied into the new cache")
	}
	if _, err := os.Stat(binary); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(binary, filepath.Join(newCache, "runtimes")) {
		t.Fatalf("expected binary in new cache, got %q", binary)
	}
}
