package runtimes

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	LlamaCPPName        = "llama.cpp"
	githubAPIURL        = "https://api.github.com/repos/ggml-org/llama.cpp/releases/latest"
	fallbackLlamaCPPTag = "b9672"
)

type RuntimeStatus struct {
	Name       string `json:"name"`
	Ready      bool   `json:"ready"`
	Version    string `json:"version,omitempty"`
	Platform   string `json:"platform"`
	BinaryPath string `json:"binary_path,omitempty"`
	Source     string `json:"source,omitempty"`
	Error      string `json:"error,omitempty"`
}

type githubRelease struct {
	TagName string              `json:"tag_name"`
	Assets  []githubAssetObject `json:"assets"`
}

type githubAssetObject struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type asset struct {
	Name string
	URL  string
}

func LlamaCPPStatus(cacheDir string) RuntimeStatus {
	status := RuntimeStatus{
		Name:     LlamaCPPName,
		Platform: platformKey(),
		Source:   "cmesh-runtime-cache",
	}
	if !preferCachedLlamaCPP() {
		if binary, err := FindSystemLlamaCPP(); err == nil {
			status.Ready = true
			status.BinaryPath = binary
			status.Source = "system-path"
			return status
		}
	}
	binary, version, err := cachedLlamaCPPBinary(cacheDir)
	if err == nil {
		status.Ready = true
		status.BinaryPath = binary
		status.Version = version
		return status
	}
	if preferCachedLlamaCPP() {
		status.Error = err.Error()
		return status
	}
	if binary, err := FindSystemLlamaCPP(); err == nil {
		status.Ready = true
		status.BinaryPath = binary
		status.Source = "system-path"
		return status
	}
	if err != nil {
		status.Error = err.Error()
		return status
	}
	return status
}

func EnsureLlamaCPP(ctx context.Context, cacheDir string) (string, RuntimeStatus, error) {
	if override, ok := llamaCPPRuntimeOverride(); ok {
		if binary, err := cachedLlamaCPPBinaryVersion(cacheDir, override.Version); err == nil {
			return binary, RuntimeStatus{
				Name:       LlamaCPPName,
				Ready:      true,
				Version:    override.Version,
				Platform:   platformKey(),
				BinaryPath: binary,
				Source:     "cmesh-runtime-override-cache",
			}, nil
		}
		return ensureLlamaCPPAsset(ctx, cacheDir, override, "cmesh-runtime-override")
	}
	if !preferCachedLlamaCPP() {
		if binary, err := FindSystemLlamaCPP(); err == nil {
			return binary, RuntimeStatus{
				Name:       LlamaCPPName,
				Ready:      true,
				Platform:   platformKey(),
				BinaryPath: binary,
				Source:     "system-path",
			}, nil
		}
	}
	if binary, version, err := cachedLlamaCPPBinary(cacheDir); err == nil {
		return binary, RuntimeStatus{
			Name:       LlamaCPPName,
			Ready:      true,
			Version:    version,
			Platform:   platformKey(),
			BinaryPath: binary,
			Source:     "cmesh-runtime-cache",
		}, nil
	}
	if binary, version, err := migrateCachedLlamaCPP(cacheDir); err == nil {
		return binary, RuntimeStatus{
			Name:       LlamaCPPName,
			Ready:      true,
			Version:    version,
			Platform:   platformKey(),
			BinaryPath: binary,
			Source:     "cmesh-runtime-cache-migrated",
		}, nil
	}
	if preferCachedLlamaCPP() {
		err := fmt.Errorf("runtime is not installed")
		status := LlamaCPPStatus(cacheDir)
		status.Error = err.Error()
		return "", status, err
	}
	release, err := fetchLatestRelease(ctx)
	if err != nil {
		status := LlamaCPPStatus(cacheDir)
		status.Error = err.Error()
		return "", status, err
	}
	selected, err := selectLlamaCPPAsset(release, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		status := LlamaCPPStatus(cacheDir)
		status.Version = release.TagName
		status.Error = err.Error()
		return "", status, err
	}
	return ensureLlamaCPPAsset(ctx, cacheDir, versionedAsset{Asset: selected, Version: release.TagName}, selected.URL)
}

type versionedAsset struct {
	Asset   asset
	Version string
}

func ensureLlamaCPPAsset(ctx context.Context, cacheDir string, selected versionedAsset, source string) (string, RuntimeStatus, error) {
	version := strings.TrimSpace(selected.Version)
	if version == "" {
		version = "custom"
	}
	dir := runtimeDir(cacheDir, version)
	if err := os.RemoveAll(dir); err != nil {
		return "", RuntimeStatus{}, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", RuntimeStatus{}, err
	}
	if err := downloadAndExtract(ctx, selected.Asset.URL, selected.Asset.Name, dir); err != nil {
		_ = os.RemoveAll(dir)
		status := RuntimeStatus{
			Name:     LlamaCPPName,
			Version:  version,
			Platform: platformKey(),
			Source:   source,
			Error:    err.Error(),
		}
		return "", status, err
	}
	if err := normalizeSharedLibraryLinks(dir); err != nil {
		_ = os.RemoveAll(dir)
		status := RuntimeStatus{
			Name:     LlamaCPPName,
			Version:  version,
			Platform: platformKey(),
			Source:   source,
			Error:    err.Error(),
		}
		return "", status, err
	}
	binary, err := findBinary(dir, llamaBinaryName())
	if err != nil {
		_ = os.RemoveAll(dir)
		status := RuntimeStatus{
			Name:     LlamaCPPName,
			Version:  version,
			Platform: platformKey(),
			Source:   source,
			Error:    err.Error(),
		}
		return "", status, err
	}
	_ = os.Chmod(binary, 0o755)
	status := RuntimeStatus{
		Name:       LlamaCPPName,
		Ready:      true,
		Version:    version,
		Platform:   platformKey(),
		BinaryPath: binary,
		Source:     source,
	}
	return binary, status, nil
}

func llamaCPPRuntimeOverride() (versionedAsset, bool) {
	url := strings.TrimSpace(os.Getenv("CMESH_LLAMA_CPP_RUNTIME_URL"))
	if url == "" {
		return versionedAsset{}, false
	}
	name := strings.TrimSpace(os.Getenv("CMESH_LLAMA_CPP_RUNTIME_NAME"))
	if name == "" {
		name = archiveNameFromURL(url)
	}
	version := strings.TrimSpace(os.Getenv("CMESH_LLAMA_CPP_RUNTIME_VERSION"))
	if version == "" {
		version = "custom-" + platformKey()
	}
	return versionedAsset{Asset: asset{Name: name, URL: url}, Version: version}, true
}

func preferCachedLlamaCPP() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CMESH_LLAMA_CPP_PREFER_CACHE"))) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func archiveNameFromURL(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if i := strings.Index(rawURL, "?"); i >= 0 {
		rawURL = rawURL[:i]
	}
	rawURL = strings.TrimRight(rawURL, "/")
	name := filepath.Base(rawURL)
	if strings.TrimSpace(name) == "" || name == "." || name == string(filepath.Separator) {
		return "llama.cpp-runtime.tar.gz"
	}
	return name
}

func FindSystemLlamaCPP() (string, error) {
	if cli, err := exec.LookPath(llamaBinaryName()); err == nil {
		return cli, nil
	}
	candidates := []string{
		"/opt/homebrew/bin/llama-cli",
		"/usr/local/bin/llama-cli",
		"/opt/local/bin/llama-cli",
		"/usr/bin/llama-cli",
	}
	if runtime.GOOS == "windows" {
		candidates = append([]string{
			`C:\Program Files\llama.cpp\llama-cli.exe`,
			`C:\Program Files\CMesh\llama-cli.exe`,
		}, candidates...)
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", exec.ErrNotFound
}

func fetchLatestRelease(ctx context.Context) (githubRelease, error) {
	tag := strings.TrimSpace(os.Getenv("CMESH_LLAMA_CPP_TAG"))
	url := githubAPIURL
	if tag != "" {
		url = "https://api.github.com/repos/ggml-org/llama.cpp/releases/tags/" + tag
	}

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return githubRelease{}, ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		release, err := fetchGitHubRelease(ctx, url)
		if err == nil {
			return release, nil
		}
		lastErr = err
	}

	if tag == "" {
		return fallbackLlamaCPPRelease(fallbackLlamaCPPTag), nil
	}
	return githubRelease{}, lastErr
}

func fetchGitHubRelease(ctx context.Context, url string) (githubRelease, error) {
	requestCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, url, nil)
	if err != nil {
		return githubRelease{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return githubRelease{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return githubRelease{}, fmt.Errorf("llama.cpp release lookup returned %s", resp.Status)
	}
	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return githubRelease{}, err
	}
	if release.TagName == "" {
		return githubRelease{}, fmt.Errorf("llama.cpp release has no tag")
	}
	return release, nil
}

func fallbackLlamaCPPRelease(tag string) githubRelease {
	base := "https://github.com/ggml-org/llama.cpp/releases/download/" + tag + "/llama-" + tag
	names := []string{
		"-bin-macos-arm64.tar.gz",
		"-bin-macos-x64.tar.gz",
		"-bin-ubuntu-x64.tar.gz",
		"-bin-ubuntu-arm64.tar.gz",
		"-bin-win-cpu-x64.zip",
		"-bin-win-cpu-arm64.zip",
	}
	release := githubRelease{TagName: tag, Assets: make([]githubAssetObject, 0, len(names))}
	for _, name := range names {
		assetName := "llama-" + tag + name
		release.Assets = append(release.Assets, githubAssetObject{
			Name:               assetName,
			BrowserDownloadURL: base + name,
		})
	}
	return release
}

func selectLlamaCPPAsset(release githubRelease, goos string, goarch string) (asset, error) {
	var want string
	switch goos + "/" + goarch {
	case "darwin/arm64":
		want = "-bin-macos-arm64.tar.gz"
	case "darwin/amd64":
		want = "-bin-macos-x64.tar.gz"
	case "linux/amd64":
		want = "-bin-ubuntu-x64.tar.gz"
	case "linux/arm64":
		want = "-bin-ubuntu-arm64.tar.gz"
	case "windows/amd64":
		want = "-bin-win-cpu-x64.zip"
	case "windows/arm64":
		want = "-bin-win-cpu-arm64.zip"
	default:
		return asset{}, fmt.Errorf("llama.cpp runtime is not available for %s/%s", goos, goarch)
	}
	for _, candidate := range release.Assets {
		if strings.HasSuffix(candidate.Name, want) && candidate.BrowserDownloadURL != "" {
			return asset{Name: candidate.Name, URL: candidate.BrowserDownloadURL}, nil
		}
	}
	return asset{}, fmt.Errorf("llama.cpp release %s has no asset for %s/%s", release.TagName, goos, goarch)
}

func downloadAndExtract(ctx context.Context, url string, name string, dir string) error {
	requestCtx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("runtime download returned %s", resp.Status)
	}
	tmp, err := os.CreateTemp("", "cmesh-runtime-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if strings.HasSuffix(name, ".zip") {
		return extractZip(tmpPath, dir)
	}
	if strings.HasSuffix(name, ".tar.gz") {
		return extractTarGz(tmpPath, dir)
	}
	return fmt.Errorf("unsupported runtime archive %q", name)
}

func extractZip(path string, dir string) error {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer reader.Close()
	for _, file := range reader.File {
		target, err := safeArchivePath(dir, file.Name)
		if err != nil {
			return err
		}
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		in, err := file.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, file.FileInfo().Mode())
		if err != nil {
			_ = in.Close()
			return err
		}
		_, copyErr := io.Copy(out, in)
		closeInErr := in.Close()
		closeOutErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeInErr != nil {
			return closeInErr
		}
		if closeOutErr != nil {
			return closeOutErr
		}
	}
	return nil
}

func extractTarGz(path string, dir string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := safeArchivePath(dir, header.Name)
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(header.Mode)); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(out, reader)
			closeErr := out.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			linkTarget := filepath.Clean(header.Linkname)
			if filepath.IsAbs(linkTarget) || linkTarget == ".." || strings.HasPrefix(linkTarget, ".."+string(os.PathSeparator)) {
				return fmt.Errorf("unsafe archive symlink %q -> %q", header.Name, header.Linkname)
			}
			_ = os.Remove(target)
			if err := os.Symlink(header.Linkname, target); err != nil {
				return err
			}
		}
	}
}

func normalizeSharedLibraryLinks(root string) error {
	if runtime.GOOS != "linux" {
		return nil
	}
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		name := entry.Name()
		idx := strings.Index(name, ".so.")
		if idx < 0 {
			return nil
		}
		suffix := name[idx+len(".so."):]
		parts := strings.Split(suffix, ".")
		if len(parts) == 0 || parts[0] == "" {
			return nil
		}
		majorName := name[:idx+len(".so.")] + parts[0]
		if err := ensureRelativeSymlink(filepath.Dir(path), majorName, name); err != nil {
			return err
		}
		baseName := name[:idx+len(".so")]
		return ensureRelativeSymlink(filepath.Dir(path), baseName, majorName)
	})
}

func ensureRelativeSymlink(dir string, linkName string, targetName string) error {
	linkPath := filepath.Join(dir, linkName)
	if info, err := os.Lstat(linkPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.Symlink(targetName, linkPath)
}

func safeArchivePath(root string, name string) (string, error) {
	clean := filepath.Clean(name)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	target := filepath.Join(root, clean)
	rootClean, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	targetClean, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	if targetClean != rootClean && !strings.HasPrefix(targetClean, rootClean+string(os.PathSeparator)) {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	return target, nil
}

func cachedLlamaCPPBinary(cacheDir string) (string, string, error) {
	root := filepath.Join(runtimeRoot(cacheDir), LlamaCPPName)
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", "", fmt.Errorf("runtime is not installed")
	}
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		if !entry.IsDir() {
			continue
		}
		version := entry.Name()
		binary, err := findBinary(filepath.Join(root, version), llamaBinaryName())
		if err == nil {
			return binary, version, nil
		}
	}
	return "", "", fmt.Errorf("runtime is not installed")
}

func cachedLlamaCPPBinaryVersion(cacheDir string, version string) (string, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		return "", fmt.Errorf("runtime version is required")
	}
	binary, err := findBinary(runtimeDir(cacheDir, version), llamaBinaryName())
	if err != nil {
		return "", fmt.Errorf("runtime is not installed")
	}
	return binary, nil
}

func migrateCachedLlamaCPP(cacheDir string) (string, string, error) {
	targetRoot := filepath.Join(runtimeRoot(cacheDir), LlamaCPPName)
	targetAbs, err := filepath.Abs(targetRoot)
	if err != nil {
		return "", "", err
	}
	for _, legacyCacheDir := range legacyCacheDirs() {
		sourceRoot := filepath.Join(runtimeRoot(legacyCacheDir), LlamaCPPName)
		sourceAbs, err := filepath.Abs(sourceRoot)
		if err != nil || sourceAbs == targetAbs {
			continue
		}
		entries, err := os.ReadDir(sourceRoot)
		if err != nil {
			continue
		}
		for i := len(entries) - 1; i >= 0; i-- {
			entry := entries[i]
			if !entry.IsDir() {
				continue
			}
			version := entry.Name()
			sourceDir := filepath.Join(sourceRoot, version)
			if _, err := findBinary(sourceDir, llamaBinaryName()); err != nil {
				continue
			}
			targetDir := filepath.Join(targetRoot, version)
			if err := copyDir(sourceDir, targetDir); err != nil {
				return "", "", err
			}
			binary, err := findBinary(targetDir, llamaBinaryName())
			if err != nil {
				return "", "", err
			}
			_ = os.Chmod(binary, 0o755)
			return binary, version, nil
		}
	}
	return "", "", fmt.Errorf("runtime is not installed")
}

func legacyCacheDirs() []string {
	var dirs []string
	if configured := strings.TrimSpace(os.Getenv("CMESH_LLAMA_CPP_LEGACY_CACHE_DIRS")); configured != "" {
		for _, item := range filepath.SplitList(configured) {
			if strings.TrimSpace(item) != "" {
				dirs = append(dirs, strings.TrimSpace(item))
			}
		}
	}
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		switch runtime.GOOS {
		case "darwin":
			dirs = append(dirs, filepath.Join(home, "Library", "Caches", "cmesh", "cache"))
		case "windows":
			if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
				dirs = append(dirs, filepath.Join(localAppData, "cmesh", "cache"))
			}
			dirs = append(dirs, filepath.Join(home, "AppData", "Local", "cmesh", "cache"))
		default:
			if xdgCache := strings.TrimSpace(os.Getenv("XDG_CACHE_HOME")); xdgCache != "" {
				dirs = append(dirs, filepath.Join(xdgCache, "cmesh", "cache"))
			}
			dirs = append(dirs, filepath.Join(home, ".cache", "cmesh", "cache"))
		}
	}
	return dirs
}

func copyDir(source string, target string) error {
	if err := os.RemoveAll(target); err != nil {
		return err
	}
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		dst := filepath.Join(target, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(dst, info.Mode().Perm())
		}
		if entry.Type()&os.ModeSymlink != 0 {
			linkTarget, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(linkTarget, dst)
		}
		return copyFile(path, dst, info.Mode().Perm())
	})
}

func copyFile(source string, target string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	src, err := os.Open(source)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return err
	}
	return dst.Close()
}

func findBinary(root string, name string) (string, error) {
	var found string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || entry.Name() != name {
			return nil
		}
		found = path
		return filepath.SkipAll
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("%s not found in runtime", name)
	}
	return found, nil
}

func runtimeRoot(cacheDir string) string {
	return filepath.Join(cacheDir, "runtimes")
}

func runtimeDir(cacheDir string, version string) string {
	return filepath.Join(runtimeRoot(cacheDir), LlamaCPPName, version)
}

func llamaBinaryName() string {
	if runtime.GOOS == "windows" {
		return "llama-cli.exe"
	}
	return "llama-cli"
}

func platformKey() string {
	return runtime.GOOS + "/" + runtime.GOARCH
}
