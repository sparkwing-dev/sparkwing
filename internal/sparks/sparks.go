package sparks

import "context"

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
