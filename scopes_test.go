package gitea

import (
	"strings"
	"testing"
)

func TestNormalizeScopes(t *testing.T) {
	cases := []struct {
		name    string
		in      []string
		want    []string
		wantErr bool
	}{
		{"single", []string{"read:repository"}, []string{"read:repository"}, false},
		{"trim+case", []string{" Write:Issue "}, []string{"write:issue"}, false},
		{"dedupe", []string{"read:user", "read:user", "write:user"}, []string{"read:user", "write:user"}, false},
		{"all", []string{"all"}, []string{"all"}, false},
		{"public-only", []string{"public-only", "read:repository"}, []string{"public-only", "read:repository"}, false},
		{"blank-only", []string{"", "  "}, nil, true},
		{"empty", nil, nil, true},
		{"invalid", []string{"read:repository", "sudo"}, nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := normalizeScopes(c.in)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Errorf("normalizeScopes(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestKnownScopesSorted(t *testing.T) {
	scopes := knownScopes()
	if len(scopes) != len(validScopes) {
		t.Fatalf("knownScopes len = %d, want %d", len(scopes), len(validScopes))
	}
	for i := 1; i < len(scopes); i++ {
		if scopes[i-1] > scopes[i] {
			t.Errorf("knownScopes not sorted at %d: %q > %q", i, scopes[i-1], scopes[i])
		}
	}
}
