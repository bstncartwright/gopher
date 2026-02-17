package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLatestReleaseAndSelectAsset(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/gopher/releases/latest" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("authorization") != "Bearer test-token" {
			t.Fatalf("missing bearer token header")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"tag_name":"v1.2.3","assets":[{"name":"gopher-linux-amd64.tar.gz","browser_download_url":"https://download/linux"},{"name":"checksums.txt","browser_download_url":"https://download/checksums"}]}`))
	}))
	defer server.Close()

	client := GitHubReleasesClient{
		Owner:   "acme",
		Repo:    "gopher",
		Token:   "test-token",
		BaseURL: server.URL,
	}
	release, err := client.LatestRelease(context.Background())
	if err != nil {
		t.Fatalf("LatestRelease() error: %v", err)
	}
	if release.TagName != "v1.2.3" {
		t.Fatalf("tag = %q, want v1.2.3", release.TagName)
	}
	asset, err := SelectAsset(release, "linux", "amd64", "gopher")
	if err != nil {
		t.Fatalf("SelectAsset() error: %v", err)
	}
	if asset.Name != "gopher-linux-amd64.tar.gz" {
		t.Fatalf("asset = %q", asset.Name)
	}
	if _, err := SelectChecksumsAsset(release); err != nil {
		t.Fatalf("SelectChecksumsAsset() error: %v", err)
	}
}
