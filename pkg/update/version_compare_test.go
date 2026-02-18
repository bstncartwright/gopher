package update

import "testing"

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		name    string
		current string
		latest  string
		want    int
		wantErr bool
	}{
		{name: "equal", current: "v1.2.3", latest: "v1.2.3", want: 0},
		{name: "current older patch", current: "v1.2.3", latest: "v1.2.4", want: -1},
		{name: "current newer minor", current: "v1.3.0", latest: "v1.2.9", want: 1},
		{name: "release newer than prerelease", current: "v1.2.3", latest: "v1.2.3-rc.1", want: 1},
		{name: "prerelease older than release", current: "v1.2.3-rc.1", latest: "v1.2.3", want: -1},
		{name: "prerelease numeric compare", current: "v1.2.3-rc.2", latest: "v1.2.3-rc.10", want: -1},
		{name: "invalid", current: "dev", latest: "v1.2.3", wantErr: true},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			got, err := CompareVersions(test.current, test.latest)
			if test.wantErr {
				if err == nil {
					t.Fatalf("CompareVersions(%q, %q) expected error", test.current, test.latest)
				}
				return
			}
			if err != nil {
				t.Fatalf("CompareVersions(%q, %q) error: %v", test.current, test.latest, err)
			}
			if got != test.want {
				t.Fatalf("CompareVersions(%q, %q) = %d, want %d", test.current, test.latest, got, test.want)
			}
		})
	}
}
