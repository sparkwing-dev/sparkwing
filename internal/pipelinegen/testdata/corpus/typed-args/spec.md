---
shape: typed-args
expect: pass
entrypoint: GenTypedArgs
---
A deploy pipeline the operator parameterizes from the command line:
`--environment` names the target environment and `--dry-run` renders the
manifests without applying them. Both must show up as real flags on
`sparkwing run`, with help text, rather than being read out of ambient
environment variables.
