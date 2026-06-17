package runtimes

import "testing"

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
