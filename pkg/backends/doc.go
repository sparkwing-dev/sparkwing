// The backend specs in this package describe where the three
// persistence surfaces live -- cache (content-addressed artifacts),
// logs (per-job log streams), and state (run records, plan snapshots,
// status). They are declared per profile in
// ~/.config/sparkwing/config.yaml; there is no standalone backends.yaml.
// State is sqlite (this machine) or controller; S3 and Postgres state are
// refused when a config loads.
//
// # Selection at process start
//
// The resolved profile's state / cache / logs specs decide each surface.
// A profile is selected by --profile NAME or by the pipeline's own
// profile: field; neither the environment nor a project default selects one.
//
// # Shape (yaml)
//
//	# ~/.config/sparkwing/config.yaml
//	profiles:
//	  laptop:
//	    state: { type: sqlite,     path: ~/.cache/sparkwing/state.db }
//	    cache: { type: filesystem, path: ~/.cache/sparkwing }
//	    logs:  { type: filesystem, path: ~/.cache/sparkwing/logs }
//
//	  prod:
//	    controller: { url: https://sparkwing.example.com }
package backends
