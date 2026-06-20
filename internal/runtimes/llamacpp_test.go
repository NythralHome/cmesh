package runtimes

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
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

func TestEnsureLlamaCPPUsesRuntimeOverrideBeforeSystemPath(t *testing.T) {
	pathDir := t.TempDir()
	systemBinary := filepath.Join(pathDir, llamaBinaryName())
	if err := os.WriteFile(systemBinary, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", pathDir)

	archive := llamaCPPTestArchive(t, "runtime/bin/"+llamaBinaryName())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archive)
	}))
	t.Cleanup(server.Close)
	t.Setenv("CMESH_LLAMA_CPP_RUNTIME_URL", server.URL+"/cmesh-llama-runtime.tar.gz")
	t.Setenv("CMESH_LLAMA_CPP_RUNTIME_VERSION", "cmesh-b9704-linux-amd64")

	binary, status, err := EnsureLlamaCPP(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if binary == systemBinary {
		t.Fatal("expected runtime override to win over system PATH")
	}
	if status.Source != "cmesh-runtime-override" || status.Version != "cmesh-b9704-linux-amd64" || !status.Ready {
		t.Fatalf("unexpected override status: %#v", status)
	}
	if !strings.Contains(binary, filepath.Join("runtimes", LlamaCPPName, "cmesh-b9704-linux-amd64")) {
		t.Fatalf("expected override binary in cache, got %q", binary)
	}
}

func TestEnsureLlamaCPPOverrideUsesCachedVersionWithoutRedownload(t *testing.T) {
	archive := llamaCPPTestArchive(t, "runtime/bin/"+llamaBinaryName())
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write(archive)
	}))
	t.Cleanup(server.Close)

	cacheDir := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	t.Setenv("CMESH_LLAMA_CPP_RUNTIME_URL", server.URL+"/cmesh-llama-runtime.tar.gz")
	t.Setenv("CMESH_LLAMA_CPP_RUNTIME_VERSION", "cmesh-b9704-linux-amd64")

	firstBinary, firstStatus, err := EnsureLlamaCPP(t.Context(), cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	secondBinary, secondStatus, err := EnsureLlamaCPP(t.Context(), cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if firstBinary != secondBinary {
		t.Fatalf("expected cached override binary reuse, got %q then %q", firstBinary, secondBinary)
	}
	if firstStatus.Source != "cmesh-runtime-override" || secondStatus.Source != "cmesh-runtime-override-cache" {
		t.Fatalf("unexpected override sources: first=%#v second=%#v", firstStatus, secondStatus)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("expected one runtime download, got %d", hits)
	}
}

func TestLlamaCPPPreferCacheDoesNotUseSystemPath(t *testing.T) {
	pathDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(pathDir, llamaBinaryName()), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", pathDir)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "xdg-cache"))
	t.Setenv("CMESH_LLAMA_CPP_LEGACY_CACHE_DIRS", "")
	t.Setenv("CMESH_LLAMA_CPP_PREFER_CACHE", "true")
	t.Setenv("CMESH_LLAMA_CPP_TAG", "test-no-download")

	_, status, err := EnsureLlamaCPP(t.Context(), t.TempDir())
	if err == nil {
		t.Fatal("expected missing cached runtime error")
	}
	if status.Ready || status.Source == "system-path" {
		t.Fatalf("expected cache-only status, got %#v", status)
	}
}

func TestNormalizeSharedLibraryLinksCreatesSONAMELinks(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux shared library symlink normalization")
	}
	dir := t.TempDir()
	libDir := filepath.Join(dir, "lib")
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(libDir, "libggml.so.0.15.1"), []byte("lib"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := normalizeSharedLibraryLinks(dir); err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(filepath.Join(libDir, "libggml.so.0")); err != nil || target != "libggml.so.0.15.1" {
		t.Fatalf("expected libggml.so.0 symlink, target=%q err=%v", target, err)
	}
	if target, err := os.Readlink(filepath.Join(libDir, "libggml.so")); err != nil || target != "libggml.so.0" {
		t.Fatalf("expected libggml.so symlink, target=%q err=%v", target, err)
	}
}

func llamaCPPTestArchive(t *testing.T, binaryName string) []byte {
	t.Helper()
	var body bytes.Buffer
	gz := gzip.NewWriter(&body)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: binaryName, Mode: 0o755, Size: int64(len("fake llama\n"))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("fake llama\n")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes()
}
