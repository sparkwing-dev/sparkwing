### Security

- **store:** Seal run argument values at rest
  Stored run requests, accepted specs and generator expansion registrations
  kept supplied arguments, declared defaults (including secret defaults) and
  masking registration values in plaintext JSON. Every argument value now moves
  into one envelope sealed with the secrets key and bound to the run's team, run
  ID and document kind; names and secret classifications stay readable. A keyed
  store whose key cannot be used refuses the run instead of storing plaintext. A
  PostgreSQL controller without a `secrets-key` credential stores run argument
  values, including secret defaults, unencrypted and warns at startup. Runs
  stored earlier still read as written. `sparkwing-controller restore prepare`
  opens sealed arguments with the serving controller's key. `sparkwing operator secrets rotate` seals them
  in place and reports `plaintext_run_arguments`, and it reseals argument
  envelopes as `run_arguments`.
