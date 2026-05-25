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
// A profile may carry a [Detect] predicate so it auto-selects when its
// environment condition matches.
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
//	  gha:
//	    detect: { env_var: GITHUB_ACTIONS, equals: "true" }
//	    state: { type: s3, bucket: sparkwing-state, prefix: "${GITHUB_REPOSITORY}/" }
//	    cache: { type: s3, bucket: sparkwing-cache, prefix: "${GITHUB_REPOSITORY}/" }
//	    logs:  { type: s3, bucket: sparkwing-logs,  prefix: "${GITHUB_REPOSITORY}/" }
package backends
