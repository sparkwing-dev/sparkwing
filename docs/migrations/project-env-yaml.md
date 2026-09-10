# Project manifests use YAML

The project manifest is `.xwing-env.yaml`. The old `.xwing-env.json` file is
removed, and Xwing rejects it even when a YAML manifest also exists.

The project, profiles, tool install argv and runtime profile retain their values.
Keep local environment value files in their existing locations. The loader and
candidate install hook continue to use Xwing's project contract.

For a worktree that still has the JSON manifest, merge the updated main branch or
convert its declarations to YAML and remove the old JSON file. Review local
manifest edits before conversion. Install the matching Xwing build before using
the new manifest. Older worktrees are not rewritten automatically.
