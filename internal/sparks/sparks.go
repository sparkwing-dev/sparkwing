package sparks

import "context"

// ResolveAndWrite is the full pipeline: load manifest, resolve, write
// overlay. Returns (true, nil) if the overlay was (re)written. Returns
// (false, nil) on the fast path (no manifest, or overlay already up to
// date).
func ResolveAndWrite(ctx context.Context, sparkwingDir string) (bool, error) {
	m, err := LoadManifest(sparkwingDir)
	if err != nil {
		return false, err
	}
	if m == nil || len(m.Libraries) == 0 {
		return false, nil
	}
	resolved, err := Resolve(ctx, m)
	if err != nil {
		return false, err
	}
	return WriteOverlay(ctx, sparkwingDir, resolved)
}
