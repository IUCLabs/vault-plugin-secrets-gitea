package gitea

import (
	"fmt"
	"sort"
	"strings"
)

// validScopes is the authoritative set of Gitea access-token scopes, matching
// go-gitea/gitea models/auth/access_token_scope.go. "all" grants every
// permission; "public-only" restricts a token to public resources. Every
// category has read: and write: forms (write implies read).
var validScopes = map[string]struct{}{
	"all":                {},
	"public-only":        {},
	"read:activitypub":   {},
	"write:activitypub":  {},
	"read:admin":         {},
	"write:admin":        {},
	"read:misc":          {},
	"write:misc":         {},
	"read:notification":  {},
	"write:notification": {},
	"read:organization":  {},
	"write:organization": {},
	"read:package":       {},
	"write:package":      {},
	"read:issue":         {},
	"write:issue":        {},
	"read:repository":    {},
	"write:repository":   {},
	"read:user":          {},
	"write:user":         {},
}

// knownScopes returns the sorted list of valid scopes, for error messages.
func knownScopes() []string {
	out := make([]string, 0, len(validScopes))
	for s := range validScopes {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// normalizeScopes trims, lower-cases and de-duplicates the requested scopes,
// rejecting any that Gitea would not recognise. Order is preserved (first
// occurrence wins) so the stored role reads back predictably.
func normalizeScopes(scopes []string) ([]string, error) {
	if len(scopes) == 0 {
		return nil, fmt.Errorf("at least one scope is required; valid scopes: %s", strings.Join(knownScopes(), ", "))
	}
	seen := make(map[string]struct{}, len(scopes))
	out := make([]string, 0, len(scopes))
	for _, raw := range scopes {
		s := strings.ToLower(strings.TrimSpace(raw))
		if s == "" {
			continue
		}
		if _, ok := validScopes[s]; !ok {
			return nil, fmt.Errorf("invalid scope %q; valid scopes: %s", raw, strings.Join(knownScopes(), ", "))
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("at least one scope is required; valid scopes: %s", strings.Join(knownScopes(), ", "))
	}
	return out, nil
}
