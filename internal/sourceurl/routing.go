package sourceurl

import "strings"

// RoutingIdentity normalizes an admitted repository for claim scope matching.
// Local snapshots and friendly displays without a routable key have no URL identity.
func RoutingIdentity(key, display string) (string, error) {
	if strings.HasPrefix(key, "local:") {
		return "", nil
	}
	if identity, found := strings.CutPrefix(key, "git:"); found {
		if normalized, err := Identity("https://" + identity); err == nil {
			return normalized, nil
		}
	}
	if display == "" {
		return "", nil
	}
	if owner, _, _ := strings.Cut(display, "/"); strings.HasPrefix(key, "gh:") && strings.Count(display, "/") == 1 && !strings.ContainsAny(owner, ".:") && !strings.Contains(display, ":") {
		display = "https://github.com/" + display
	}
	if !strings.Contains(display, ":") {
		display = "https://" + display
	}
	identity, err := Identity(display)
	if err != nil {
		return "", nil
	}
	return identity, nil
}
