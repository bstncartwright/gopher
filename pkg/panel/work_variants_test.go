package panel

import "testing"

func TestNormalizeWorkVariant(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{input: "v1", want: "v1"},
		{input: " V8 ", want: "v8"},
		{input: "work", want: ""},
		{input: "v9", want: ""},
	}

	for _, tc := range tests {
		if got := normalizeWorkVariant(tc.input); got != tc.want {
			t.Fatalf("normalizeWorkVariant(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestBuildWorkVariantLinks(t *testing.T) {
	t.Parallel()

	links := buildWorkVariantLinks("v3", "sess alpha", "view=raw&noise=all")
	if len(links) != len(workVariantCatalog) {
		t.Fatalf("links length = %d, want %d", len(links), len(workVariantCatalog))
	}
	if links[2].Href != "/v3/admin/work/sess%20alpha?view=raw&noise=all" {
		t.Fatalf("active href = %q", links[2].Href)
	}
	if !links[2].Active {
		t.Fatalf("expected v3 link to be active")
	}
	if links[0].Href != "/v1/admin/work/sess%20alpha?view=raw&noise=all" {
		t.Fatalf("v1 href = %q", links[0].Href)
	}
}

func TestNewServerParsesVariantAssets(t *testing.T) {
	t.Parallel()

	if _, err := NewServer(ServerOptions{ListenAddr: "127.0.0.1:0"}); err != nil {
		t.Fatalf("NewServer() error: %v", err)
	}
}
