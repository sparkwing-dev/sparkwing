// The backend specs in this package describe where a profile's run
// records (state) and secrets live. They are declared per profile in
// ~/.config/sparkwing/config.yaml or .sparkwing/sparkwing.yaml; there is
// no standalone backends.yaml. State is sqlite (this machine) or
// controller; S3 and Postgres state are refused when a config loads.
//
// # Selection at process start
//
// The resolved profile's state spec decides where a read verb looks. A
// profile is selected by --profile NAME or by the pipeline's own
// profile: field; neither the environment nor a project default selects one.
//
// # Shape (yaml)
//
//	# ~/.config/sparkwing/config.yaml
//	profiles:
//	  laptop:
//	    state: { type: sqlite, path: ~/.cache/sparkwing/state.db }
//
//	  prod:
//	    controller: { url: https://sparkwing.example.com }
package backends
