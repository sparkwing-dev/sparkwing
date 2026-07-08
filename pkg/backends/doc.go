// The backend specs in this package describe where the three
// persistence surfaces live -- cache (content-addressed artifacts and
// compiled pipeline binaries), logs (per-job log streams), and state
// (run records, plan snapshots, status). They are declared per profile
// in ~/.config/sparkwing/profiles.yaml; there is no standalone
// backends.yaml.
//
// # Selection at process start
//
//  1. Per-target overlay (a pipeline's targets.<name>.backend, applied
//     via [LayerSurfaces])
//  2. The resolved profile's state / cache / logs specs
//
// A profile is selected explicitly (--profile NAME) or via the project's
// defaults.profile; there is no environment-based auto-selection.
//
// # Shape (yaml)
//
//	# ~/.config/sparkwing/profiles.yaml
//	profiles:
//	  laptop:
//	    state: { type: sqlite,     path: ~/.cache/sparkwing/state.db }
//	    cache: { type: filesystem, path: ~/.cache/sparkwing }
//	    logs:  { type: filesystem, path: ~/.cache/sparkwing/logs }
//
//	  shared:
//	    state: { type: s3, bucket: sparkwing-state, prefix: "team/" }
//	    cache: { type: s3, bucket: sparkwing-cache, prefix: "team/" }
//	    logs:  { type: s3, bucket: sparkwing-logs,  prefix: "team/" }
package backends
