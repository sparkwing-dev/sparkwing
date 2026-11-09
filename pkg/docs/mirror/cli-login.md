<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing login

Every `sparkwing login` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing login`

Sign in through the browser and save a CLI token

Sign this machine in through the browser and save a team member token in the profile. The command prints a code and the controller's /device page; open that page where you are signed in to the console, type the code, check the machine name, team and grants, and approve. Nothing is copied out of the browser.

The token carries the grants --scope names, or by default your role's run and log grants: runs.read and logs.read, plus runs.write, runs.control and approvals.write for editors and owners. It never carries team.admin or more than you hold. It expires after 90 days and ends when the approving browser session signs out.

The command needs stdin and stderr on a terminal, so a code never lands in a script's log. It defaults --team to the saved profile token's team when that token is still valid on the selected controller; without a known team, approval remains unrestricted. It prints the approving team before saving; --team makes the controller refuse an approver from another team and the CLI refuse a token for another. The code expires after 10 minutes, works once, and stops working when the command is interrupted. Denying it in the browser ends the command with an error. An existing profile for the same controller keeps its name and default flag and gets the new token; its previous member token is revoked. --force points an existing profile at a different controller.

JSON and plain output match profiles show; the token is never printed.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--controller URL` | Controller URL to sign in to; defaults to the URL saved in --profile. |
| `--profile NAME` | Profile to save the token in; defaults to the controller host. |
| `--machine NAME` | Name the approval page shows for this machine; defaults to the host name. |
| `--team TEAM` | Team this machine may join. Defaults to the saved profile token's team. An approver from another team is refused. |
| `--scope GRANT` | Grants to ask for; defaults to every run and log grant your role holds. Values: runs.read, logs.read, runs.write, runs.control, approvals.write. Repeatable; comma-separated values are accepted. |
| `--force` | Point an existing profile at a different controller. |
