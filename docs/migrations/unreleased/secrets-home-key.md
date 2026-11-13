## Local secrets keys follow the state home

**Before:** a redirected `SPARKWING_HOME` without an explicit key selector
could read the shared config-directory key. Its first seal failed when the
config-write guard refused to create that key outside the redirected home.

**After:** a new home creates its own `secrets.key` with mode `0600` on first
need, including controller startup. Generated and refreshed systemd and launchd
units pin their selected key file. Explicit `SPARKWING_SECRETS_KEY_FILE`
selections stay in use. The default `~/.sparkwing` home retains an existing
config-directory key even when a home-local key also exists. It never creates
another key while that config key exists.

An older redirected home that used the shared config key now refuses to open
its sealed data until that file is selected explicitly. The refusal names both
the home-local file it tried and the shared config key it skipped. It prints
the exact `SPARKWING_SECRETS_KEY_FILE=<path>` assignment that restores access.
For the usual Linux config path:

```sh
export SPARKWING_SECRETS_KEY_FILE="$HOME/.config/sparkwing/secrets.key"
sparkwing secrets list
```

Use the path in the refusal when `XDG_CONFIG_HOME` or the platform config path
differs. Keep that selector in the environment used to start this home's
controller. Reinstall an installed controller or schedule service after changing
its selectors. Stop an on-demand controller before starting it with a changed
selector. A missing or wrong key is never replaced over sealed data. Keep each
home's key with its backups.

A wrong `SPARKWING_SECRETS_KEY` value overrides every file selector. Replace
that value with the original key, or unset it before using a file. Its refusal
names the inline source and explains that precedence. Standalone controller
key failures retain their credential-file repair instead of suggesting local
CLI environment selectors. Secret-list API responses preserve these repairs.
