// Package sources loads the sources: section of
// .sparkwing/sparkwing.yaml -- the block that names the secret/config
// backends a pipeline target can bind to. Each entry describes one
// backend (a profile's controller vault, the macOS keychain, a local
// dotenv file, or the process environment) and is referenced by name
// from a pipeline target's source field.
//
// The block's `default:` key names the source used when a pipeline
// target doesn't bind to a named source explicitly; the runtime
// resolves the default per-call.
//
// # Loading
//
// [Load] reads one file; [Resolve] returns one [Source] by name;
// [Names] lists every declared source. Type discriminators are
// exported as [TypeProfile], [TypeMacosKeychain], [TypeFile], and
// [TypeEnv].
//
// # Shape (yaml)
//
//	# .sparkwing/sparkwing.yaml
//	sources:
//	  default: team-vault
//	  sources:
//	    team-vault:
//	      type: profile
//	      profile: shared        # profile name from profiles.yaml
//	    prod-vault:
//	      type: profile
//	      profile: prod
//	    local-keychain:
//	      type: macos-keychain
//	      service: sparkwing-pi
//	    dotenv:
//	      type: file
//	      path: .sparkwing/secrets.local.env
//	    shell-env:
//	      type: env
//	      prefix: SW_
package sources
