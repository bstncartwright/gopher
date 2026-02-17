package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type ReleaseAsset struct {
	Name               string `json:"name"`
	URL                string `json:"url"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func (a ReleaseAsset) DownloadURL() string {
	url := strings.TrimSpace(a.URL)
	if url != "" {
		return url
	}
	return strings.TrimSpace(a.BrowserDownloadURL)
}

type Release struct {
	TagName string         `json:"tag_name"`
	Assets  []ReleaseAsset `json:"assets"`
}

type GitHubReleasesClient struct {
	Owner      string
	Repo       string
	Token      string
	BaseURL    string
	HTTPClient *http.Client
}

func (c GitHubReleasesClient) LatestRelease(ctx context.Context) (Release, error) {
	owner := strings.TrimSpace(c.Owner)
	repo := strings.TrimSpace(c.Repo)
	token := strings.TrimSpace(c.Token)
	if owner == "" || repo == "" {
		return Release{}, fmt.Errorf("github owner and repo are required")
	}
	if token == "" {
		return Release{}, fmt.Errorf("github token is required")
	}

	baseURL := strings.TrimSpace(c.BaseURL)
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}

	endpoint := fmt.Sprintf("%s/repos/%s/%s/releases/latest", strings.TrimRight(baseURL, "/"), owner, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Release{}, fmt.Errorf("create github latest release request: %w", err)
	}
	req.Header.Set("accept", "application/vnd.github+json")
	req.Header.Set("authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("request latest github release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return Release{}, fmt.Errorf("github latest release status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var out Release
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Release{}, fmt.Errorf("decode latest github release: %w", err)
	}
	if strings.TrimSpace(out.TagName) == "" {
		return Release{}, fmt.Errorf("github latest release missing tag_name")
	}
	return out, nil
}

func SelectAsset(release Release, goos, goarch, pattern string) (ReleaseAsset, error) {
	osToken := strings.TrimSpace(goos)
	archToken := strings.TrimSpace(goarch)
	filter := strings.TrimSpace(pattern)
	for _, asset := range release.Assets {
		name := strings.ToLower(strings.TrimSpace(asset.Name))
		if name == "" {
			continue
		}
		if osToken != "" && !strings.Contains(name, strings.ToLower(osToken)) {
			continue
		}
		if archToken != "" && !strings.Contains(name, strings.ToLower(goarch)) {
			continue
		}
		if filter != "" && !strings.Contains(name, strings.ToLower(filter)) {
			continue
		}
		return asset, nil
	}
	return ReleaseAsset{}, fmt.Errorf("no release asset matched os=%q arch=%q pattern=%q", goos, goarch, pattern)
}

func SelectChecksumsAsset(release Release) (ReleaseAsset, error) {
	for _, asset := range release.Assets {
		name := strings.ToLower(strings.TrimSpace(asset.Name))
		if strings.Contains(name, "checksums") || strings.HasSuffix(name, ".sha256") {
			return asset, nil
		}
	}
	return ReleaseAsset{}, fmt.Errorf("no checksums asset found")
}

func DownloadWithToken(ctx context.Context, httpClient *http.Client, url, token string) ([]byte, error) {
	if strings.TrimSpace(url) == "" {
		return nil, fmt.Errorf("download url is required")
	}
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("github token is required")
	}
	client := httpClient
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create download request: %w", err)
	}
	req.Header.Set("accept", "application/octet-stream")
	req.Header.Set("authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download release asset: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return nil, fmt.Errorf("asset download status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	blob, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read asset body: %w", err)
	}
	return blob, nil
}
