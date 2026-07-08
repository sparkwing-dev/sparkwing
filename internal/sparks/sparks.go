package sparks

import "context"

// ResolveAndWrite is the full pipeline: resolve the supplied manifest
// and write the overlay modfile. Returns (true, nil) if the overlay was
// (re)written. Returns (false, nil) on the fast path (nil/empty manifest,
// or overlay already up to date). The caller loads the manifest from the
// project's sparkwing.yaml (via projectconfig.LoadSparksManifest).
func ResolveAndWrite(ctx context.Context, sparkwingDir string, m *Manifest) (bool, error) {
	if m == nil || len(m.Libraries) == 0 {
		return false, nil
	}
	resolved, err := Resolve(ctx, m)
	if err != nil {
		return false, err
	}
	return WriteOverlay(ctx, sparkwingDir, resolved)
}
