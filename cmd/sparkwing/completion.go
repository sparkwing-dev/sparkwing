// Shell-completion script emitter. All behavior is driven off
// help_registry.go so tab completion and --help describe the same
// command tree; new commands are picked up automatically.
package main

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/profile"
)

func runCompletion(args []string) error {
	fs := flag.NewFlagSet(cmdCompletion.Path, flag.ContinueOnError)
	shell := fs.String("shell", "", "shell to emit completion for (bash | zsh | fish)")
	if err := parseAndCheck(cmdCompletion, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		PrintHelp(cmdCompletion, os.Stderr)
		return fmt.Errorf("completion: unexpected positional %q (use --shell)", fs.Arg(0))
	}
	if *shell == "" {
		PrintHelp(cmdCompletion, os.Stderr)
		return errors.New("completion: --shell is required (bash | zsh | fish)")
	}
	switch *shell {
	case "bash":
		fmt.Print(renderBash())
	case "zsh":
		fmt.Print(renderZsh())
	case "fish":
		fmt.Print(renderFish())
	default:
		return fmt.Errorf("completion: unknown shell %q (expected bash|zsh|fish)", *shell)
	}
	return nil
}

// Hidden helpers below print one entry per line and exit 0 quietly;
// completion scripts rely on empty output to mean "nothing to offer".

func runInternalCompleteProfiles(_ []string) error {
	path, err := profile.DefaultPath()
	if err != nil {
		return nil //nolint:nilerr // silent failure is correct for completion context
	}
	cfg, err := profile.Load(path)
	if err != nil {
		return nil //nolint:nilerr
	}
	for _, n := range cfg.Names() {
		fmt.Println(n)
	}
	return nil
}

func runInternalCompletePipelines(_ []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return nil //nolint:nilerr
	}
	shortByName := map[string]string{}
	helpByName := map[string]string{}
	if sparkwingDir, ok := walkUpForSparkwing(cwd); ok {
		if schema, serr := readDescribeCache(sparkwingDir); serr == nil {
			for _, dp := range schema {
				if dp.Short != "" {
					shortByName[dp.Name] = dp.Short
				}
				if dp.Help != "" {
					helpByName[dp.Name] = dp.Help
				}
			}
		}
	}
	// 4 tab-separated columns: name, group, kind, desc.
	type completionRow struct {
		name  string
		group string
		kind  string // "pipeline", "command", "script"
		desc  string
	}
	var rows []completionRow
	pipelineNames := map[string]struct{}{}
	if _, cfg, derr := pipelines.Discover(cwd); derr == nil && cfg != nil {
		for _, p := range cfg.Pipelines {
			pipelineNames[p.Name] = struct{}{}
			if p.Hidden {
				continue
			}
			defaultGroup := "Manual"
			if hasAutoTrigger(p.On) {
				defaultGroup = "Triggered"
			}
			group := p.Group
			if group == "" {
				group = defaultGroup
			}
			rows = append(rows, completionRow{
				name:  p.Name,
				group: group,
				desc:  shortPipelineHint(shortByName[p.Name], helpByName[p.Name], p),
			})
		}
	}
	// First-seen group order; alphabetic within each group.
	groupOrder := []string{}
	byGroup := map[string][]completionRow{}
	for _, r := range rows {
		if _, seen := byGroup[r.group]; !seen {
			groupOrder = append(groupOrder, r.group)
		}
		byGroup[r.group] = append(byGroup[r.group], r)
	}
	for _, g := range groupOrder {
		list := byGroup[g]
		sort.Slice(list, func(i, j int) bool { return list[i].name < list[j].name })
		for _, r := range list {
			fmt.Printf("%s\t%s\t%s\t%s\n",
				strings.ReplaceAll(r.name, "\t", " "),
				r.group,
				r.kind,
				strings.ReplaceAll(r.desc, "\t", " "))
		}
	}
	return nil
}

// runInternalCompleteFlags emits "--flag\tGroupName\tDescription" per
// flag for the leaf at the given argv path. Walks longest prefix to
// shortest so multi-word leaves win.
func runInternalCompleteFlags(args []string) error {
	if len(args) == 0 {
		return nil
	}
	leaves := leafCommands()
	for n := len(args); n >= 1; n-- {
		key := strings.Join(args[:n], " ")
		cmd, ok := leaves[key]
		if !ok {
			continue
		}
		var flags []FlagSpec
		for _, f := range cmd.Flags {
			if f.Hidden {
				continue
			}
			flags = append(flags, f)
		}
		if !hasFlagNamed(flags, "help") {
			flags = append(flags, helpFlag)
		}
		groups := groupFlagsForHelp(flags, cmd.GroupOrder)
		for _, g := range groups {
			for _, f := range g.flags {
				desc := requirementTag(f.Required, f.RequiredWhen) + f.Desc
				desc = strings.ReplaceAll(desc, "\t", " ")
				group := strings.ReplaceAll(g.name, "\t", " ")
				fmt.Printf("--%s\t%s\t%s\n", f.Name, group, desc)
			}
		}
		return nil
	}
	return nil
}

// requirementTag returns a leading "[required]/[conditional]/[optional]"
// marker. Plain text — ANSI in compadd descriptions corrupts zsh redraws.
func requirementTag(required bool, requiredWhen string) string {
	switch {
	case required:
		return "[required] "
	case requiredWhen != "":
		return "[conditional] "
	default:
		return "[optional] "
	}
}

// runInternalCompletePipelineFlags emits typed flags for one pipeline
// from the describe cache. Silent on cache miss; caller falls back.
func runInternalCompletePipelineFlags(args []string) error {
	if len(args) != 1 {
		return nil
	}
	pipelineName := args[0]
	cwd, err := os.Getwd()
	if err != nil {
		return nil //nolint:nilerr
	}
	sparkwingDir, ok := walkUpForSparkwing(cwd)
	if !ok {
		return nil
	}
	schema, err := pipelineFlagsFromCache(sparkwingDir, pipelineName)
	if err == nil && len(schema) > 0 {
		for _, a := range schema {
			group := "Pipeline Args"
			desc := requirementTag(a.Required, "") + a.Desc
			if a.Desc == "" {
				desc = requirementTag(a.Required, "") + a.Type
			}
			desc = strings.ReplaceAll(desc, "\t", " ")
			fmt.Printf("--%s\t%s\t%s\n", a.Name, group, desc)
		}
		return nil
	}
	return nil
}

// walkUpForSparkwing mirrors findSparkwingDir with bool-not-error
// return so completion can silently fall through.
func walkUpForSparkwing(start string) (string, bool) {
	dir := start
	for {
		candidate := strings.TrimRight(dir, "/") + "/.sparkwing"
		if fi, err := os.Stat(candidate); err == nil && fi.IsDir() {
			if _, err := os.Stat(candidate + "/main.go"); err == nil {
				return candidate, true
			}
		}
		parent := strings.TrimRight(dir, "/")
		if idx := strings.LastIndex(parent, "/"); idx >= 0 {
			parent = parent[:idx]
			if parent == "" {
				parent = "/"
			}
		} else {
			return "", false
		}
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// runInternalCompleteHint emits "placeholder\trequirement\tdescription"
// for the next positional. Empty output = nothing to hint.
func runInternalCompleteHint(args []string) error {
	leaves := leafCommands()
	for n := len(args); n >= 1; n-- {
		key := strings.Join(args[:n], " ")
		cmd, ok := leaves[key]
		if !ok {
			continue
		}
		typed := len(args) - n
		if typed >= len(cmd.PosArgs) {
			return nil
		}
		p := cmd.PosArgs[typed]
		req := "optional"
		if p.Required {
			req = "required"
		}
		fmt.Printf("%s\t%s\t%s\n",
			strings.ReplaceAll(p.Name, "\t", " "),
			req,
			strings.ReplaceAll(p.Desc, "\t", " "))
		return nil
	}
	return nil
}

func runInternalCompleteVerbs(args []string) error {
	key := strings.Join(args, " ")
	parents := parentCommands()
	cmd, ok := parents[key]
	if !ok {
		return nil
	}
	for _, s := range completableSubcommands(cmd) {
		fmt.Printf("%s\t%s\n",
			strings.ReplaceAll(s.Name, "\t", " "),
			strings.ReplaceAll(s.Synopsis, "\t", " "))
	}
	return nil
}

// hasAutoTrigger = any non-Manual trigger.
func hasAutoTrigger(t pipelines.Triggers) bool {
	return t.Push != nil ||
		t.Webhook != nil ||
		t.Schedule != "" ||
		t.Deploy != nil ||
		t.PreHook != nil ||
		t.PostHook != nil
}

// shortPipelineHint: prefer ShortHelp, then truncated Help, then trigger summary.
func shortPipelineHint(short, help string, p pipelines.Pipeline) string {
	if s := flattenOneLine(short); s != "" {
		return s
	}
	if h := flattenOneLine(help); h != "" {
		return h
	}
	if t := summarizePipelineTriggers(p.On); t != "" {
		return t
	}
	return "pipeline"
}

func flattenOneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return ""
	}
	const maxLen = 80
	if len(s) > maxLen {
		s = strings.TrimSpace(s[:maxLen-1]) + "…"
	}
	return s
}

func summarizePipelineTriggers(t pipelines.Triggers) string {
	var bits []string
	if t.Push != nil {
		if len(t.Push.Branches) > 0 {
			bits = append(bits, "push="+strings.Join(t.Push.Branches, ","))
		} else {
			bits = append(bits, "push")
		}
	}
	if t.Webhook != nil {
		bits = append(bits, "webhook="+t.Webhook.Path)
	}
	if t.Schedule != "" {
		bits = append(bits, "schedule="+t.Schedule)
	}
	if t.Deploy != nil {
		bits = append(bits, "deploy")
	}
	if t.PreHook != nil {
		bits = append(bits, "pre-commit")
	}
	if t.PostHook != nil {
		bits = append(bits, "pre-push")
	}
	if t.Manual != nil && len(bits) == 0 {
		bits = append(bits, "manual")
	}
	return strings.Join(bits, " ")
}

// leafCommands keys leaf Commands by argv path (Path minus "sparkwing ").
func leafCommands() map[string]Command {
	out := make(map[string]Command, len(allCommands))
	for _, c := range allCommands {
		if len(c.Subcommands) > 0 {
			continue
		}
		key := strings.TrimPrefix(c.Path, "sparkwing ")
		if key == "sparkwing" {
			continue
		}
		out[key] = *c
	}
	return out
}

// parentCommands keys parent Commands by argv path; "" = top-level.
func parentCommands() map[string]Command {
	out := make(map[string]Command, len(allCommands))
	for _, c := range allCommands {
		if len(c.Subcommands) == 0 {
			continue
		}
		key := strings.TrimPrefix(c.Path, "sparkwing ")
		if key == "sparkwing" {
			key = ""
		}
		out[key] = *c
	}
	return out
}

func topLevelSubcommands() []SubcommandRef {
	return completableSubcommands(cmdSparkwing)
}

func parentNames() []string {
	var out []string
	for k := range parentCommands() {
		if k == "" {
			continue
		}
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func renderBash() string {
	var b strings.Builder
	b.WriteString(`# sparkwing bash completion
#
# Pure-bash: deliberately does NOT use _init_completion or
# _get_comp_words_by_ref from bash-completion. That package isn't
# shipped with Git Bash on Windows (and isn't always installed on
# minimal Linux setups), so depending on it would silently break
# the completion for those users. The COMP_WORDS / COMP_CWORD
# accessors below cover the same surface for the simple flag /
# value matching we do.
_sparkwing_complete() {
    local cur prev cword
    local -a words
    cur="${COMP_WORDS[COMP_CWORD]:-}"
    if (( COMP_CWORD > 0 )); then
        prev="${COMP_WORDS[COMP_CWORD-1]}"
    else
        prev=""
    fi
    words=("${COMP_WORDS[@]}")
    cword=$COMP_CWORD

    # Reconstruct the command argv (everything except the current
    # word and any word starting with '-'). The result is the path
    # we feed to the internal completion helpers.
    local -a swpath
    local w
    local i
    for (( i=1; i<cword; i++ )); do
        w="${words[i]}"
        [[ "$w" == -* ]] && continue
        swpath+=("$w")
    done

    # Flag completion: current word starts with '-'.
    if [[ "$cur" == -* ]]; then
        local -a out
        mapfile -t out < <(sparkwing _complete-flags "${swpath[@]}" 2>/dev/null | cut -f1)
        COMPREPLY=( $(compgen -W "${out[*]}" -- "$cur") )
        return
    fi

    # Value completion for --on profile names.
    if [[ "$prev" == "--on" ]]; then
        local names
        names=$(sparkwing _complete-profiles 2>/dev/null)
        COMPREPLY=( $(compgen -W "$names" -- "$cur") )
        return
    fi

    # Value completion for --pipeline pipeline names.
    if [[ "$prev" == "--pipeline" ]]; then
        local names
        names=$(sparkwing _complete-pipelines 2>/dev/null | cut -f1)
        COMPREPLY=( $(compgen -W "$names" -- "$cur") )
        return
    fi

    # Subcommand / verb: ask the binary what children are legal at
    # this depth. Empty path -> top-level subcommands. Value completion
    # for specific flags (above: --on, --pipeline) happens before this
    # block.
    local -a kids
    mapfile -t kids < <(sparkwing _complete-verbs "${swpath[@]}" 2>/dev/null | cut -f1)
    if (( ${#kids[@]} > 0 )); then
        COMPREPLY=( $(compgen -W "${kids[*]}" -- "$cur") )
        return
    fi

    # Leaf command + cur is empty: offer flag names (without making the
    # user type "-" first). On macOS/Linux the bash-completion package
    # provides this UX automatically; pure bash on Git Bash doesn't,
    # so we replicate it here. Filtering by "$cur" is a no-op when
    # empty, but keeps things tidy if the user typed a partial flag.
    local -a leafFlags
    mapfile -t leafFlags < <(sparkwing _complete-flags "${swpath[@]}" 2>/dev/null | cut -f1)
    if (( ${#leafFlags[@]} > 0 )); then
        COMPREPLY=( $(compgen -W "${leafFlags[*]}" -- "$cur") )
    fi
}
complete -F _sparkwing_complete sparkwing

# wing is the pipeline-runner symlink (or wing.exe copy on Windows).
# Completion shape: first positional = pipeline name; after that,
# flag completion via the shared sparkwing _complete-flags helper
# (wing inherits sparkwing-run flags). Without the cword==1 guard,
# every TAB after the pipeline re-suggested every pipeline name,
# producing "wing release release release ...".
_wing_complete() {
    local cur
    cur="${COMP_WORDS[COMP_CWORD]:-}"

    if (( COMP_CWORD == 1 )); then
        local pipes
        pipes=$(sparkwing _complete-pipelines 2>/dev/null | cut -f1)
        COMPREPLY=( $(compgen -W "$pipes" -- "$cur") )
        return
    fi

    # Past the pipeline name: complete flags. wing accepts the same
    # flags as "sparkwing run", so reuse that flag list. Pipeline-
    # specific flags would require shelling to the pipeline binary
    # with --describe; out of scope for tab completion.
    if [[ "$cur" == -* ]]; then
        local -a out
        mapfile -t out < <(sparkwing _complete-flags run 2>/dev/null | cut -f1)
        COMPREPLY=( $(compgen -W "${out[*]}" -- "$cur") )
    fi
}
complete -F _wing_complete wing
`)
	return b.String()
}

func renderZsh() string {
	var b strings.Builder
	b.WriteString(`#compdef sparkwing
# sparkwing zsh completion
# Usage:
#   autoload -U compinit; compinit
#   source <(sparkwing completion zsh)

# Enable group-name rendering and a format for group descriptions so
# compadd's -X explanation text is shown as a bold/colored header
# instead of just "<groupname>:". Scoped to the sparkwing/wing
# completion contexts so the user's other completions stay untouched.
# %d is the -X explanation (which itself contains %F/%B/etc. prompt
# escapes we emit from _sparkwing_complete_flags); %b%f reset bold
# and foreground color so the matches below render in the default
# style. Without this zstyle, compadd -X prints the raw explanation
# unformatted.
zstyle ':completion:*:sparkwing:*:descriptions' format '%d%b%f'
zstyle ':completion:*:wing:*:descriptions'      format '%d%b%f'
zstyle ':completion:*:sparkwing:*'              group-name ''
zstyle ':completion:*:wing:*'                   group-name ''
# list-colors was tried here for coloring [pipeline]/[command]/[script]
# and [required]/[optional] markers but had no effect: zsh's list-colors
# matches against the match name (compadd -a), not the display string
# (compadd -d), and our kind/required markers live in the display.
# Coloring would require restructuring so the marker is part of the
# match name, which changes what's inserted on <Enter>. Not worth it.

_sparkwing() {
    local -a swpath
    local w
    local i
    # Rebuild the non-flag argv path up to (but not including) the
    # current word. Feeds the _complete-* hidden helpers.
    for (( i=2; i<CURRENT; i++ )); do
        w="${words[i]}"
        [[ "$w" == -* ]] && continue
        swpath+=("$w")
    done

    # Flag completion: current word starts with '-'.
    if [[ "${words[CURRENT]}" == -* ]]; then
        _sparkwing_complete_flags "${swpath[@]}"
        return
    fi

    # Value completion: --on <TAB> -> profile names.
    if [[ ${CURRENT} -ge 2 && "${words[CURRENT-1]}" == "--on" ]]; then
        local -a profs
        profs=( ${(f)"$(sparkwing _complete-profiles 2>/dev/null)"} )
        _describe -t profiles 'profile' profs
        return
    fi

    # Value completion: --pipeline <TAB> -> pipeline names (the catalog
    # of pipelines + scripts). Fires anywhere --pipeline precedes the
    # cursor, so describe/explain/new all share the same menu.
    if [[ ${CURRENT} -ge 2 && "${words[CURRENT-1]}" == "--pipeline" ]]; then
        _sparkwing_complete_pipelines
        return
    fi

    # Positional completion for "sparkwing run <TAB>". Run is the
    # one verb in the sparkwing surface that takes the pipeline as a
    # positional rather than via --pipeline; this branch makes its
    # tab-complete match the wing shortcut. Fires when the path so
    # far is exactly ["run"] and we haven't typed a pipeline name
    # yet (so words[2] is the cursor or empty).
    if (( ${#swpath[@]} == 1 )) && [[ "${swpath[1]}" == "run" ]]; then
        _sparkwing_complete_pipelines
        return
    fi

    # Otherwise: try verbs / subcommands at this depth. If that
    # yields nothing (we're past the leaf), fall through to a
    # positional-argument hint + flag list so the operator sees both
    # what's expected next AND what flags the leaf accepts. Flags are
    # included even when words[CURRENT] isn't '-*' so that a bare
    # <TAB> after a leaf reveals the flag menu without forcing the
    # operator to type '-' first.
    if ! _sparkwing_complete_verbs "${swpath[@]}"; then
        _sparkwing_positional_hint "${swpath[@]}"
        _sparkwing_complete_flags "${swpath[@]}"
    fi
}

# _sparkwing_positional_hint renders a non-clickable hint in the
# completion menu describing the next positional argument the cursor
# is sitting on. Uses zsh's _message so the text shows up but TAB
# doesn't try to auto-select a value (there is no enumerable set).
_sparkwing_positional_hint() {
    local line name req desc
    line=$(sparkwing _complete-hint "$@" 2>/dev/null)
    if [[ -z "$line" ]]; then
        return 1
    fi
    name="${line%%$'\t'*}"
    line="${line#*$'\t'}"
    req="${line%%$'\t'*}"
    desc="${line#*$'\t'}"
    if [[ -n "$desc" ]]; then
        _message -r "$name  [$req]  $desc"
    else
        _message -r "$name  [$req]"
    fi
    return 0
}

# _sparkwing_complete_verbs queries the binary for legal children at
# the given path and renders them with per-item descriptions. Empty
# path -> top-level subcommands.
_sparkwing_complete_verbs() {
    local -a names descs
    local line name desc
    while IFS= read -r line; do
        name="${line%%$'\t'*}"
        desc="${line#*$'\t'}"
        names+=("$name")
        if [[ -z "$desc" || "$desc" == "$name" ]]; then
            descs+=("$name")
        else
            descs+=("${(r:14:: :)name}  $desc")
        fi
    done < <(sparkwing _complete-verbs "$@" 2>/dev/null)
    if (( ${#names[@]} > 0 )); then
        compadd -l -d descs -a names
        return 0
    fi
    return 1
}

# _sparkwing_complete_flags queries the binary for flag names +
# descriptions (including [required]/[conditional] prefixes) for the
# leaf at the given path. Falls through silently when no leaf matches.
_sparkwing_complete_flags() {
    # Helper emits three tab-separated fields per line:
    #     --flag<TAB>GroupName<TAB>Description
    # We bucket by GroupName in insertion order so zsh's _describe
    # renders one labeled section per group -- operators see the same
    # "Source", "System", "Other" headers the --help page uses.
    local -A _sw_group_names _sw_group_descs
    local -a _sw_group_order
    local line name group desc label

    _sw_absorb_flag_line() {
        name="${line%%$'\t'*}"
        line="${line#*$'\t'}"
        group="${line%%$'\t'*}"
        desc="${line#*$'\t'}"
        [[ -z "$group" ]] && group="Other"
        if [[ -z "${_sw_group_names[$group]-}" ]]; then
            _sw_group_order+=("$group")
            _sw_group_names[$group]=""
            _sw_group_descs[$group]=""
        fi
        _sw_group_names[$group]+="$name"$'\n'
        if [[ -z "$desc" || "$desc" == "$name" ]]; then
            label="$name"
        else
            # Truncate desc so the rendered row never exceeds terminal
            # width. All descs are plain text now (requirementTag
            # stopped emitting ANSI), so simple byte-length math is
            # correct -- no ANSI-strip step needed. Keeping ANSI out
            # of compadd display strings also prevents the duplicated-
            # row corruption zsh's completion engine hits on small-
            # terminal menu redraws.
            local _sw_indent=24
            local _sw_max=$(( ${COLUMNS:-80} - _sw_indent - 1 ))
            if (( _sw_max > 10 )) && (( ${#desc} > _sw_max )); then
                desc="${desc[1,_sw_max-1]}…"
            fi
            label="${(r:22:: :)name}  $desc"
        fi
        _sw_group_descs[$group]+="$label"$'\n'
    }

    # Pipeline-specific flags go FIRST so the "Pipeline Args" group
    # renders at the top of the menu -- operators can tab-cycle
    # directly to the per-pipeline knobs without scrolling past the
    # wing-owned plumbing (--on/--from/--config/...) every time.
    # Two invocation paths carry a pipeline name at a known
    # position:
    #   wing <pipeline> --<TAB>          leaf "wing", pipeline at $2
    #   sparkwing run <pipeline> --<TAB> leaf "run",  pipeline at $2
    # (Pre-v0.42 there was also "pipelines run <pipeline>" with the
    # pipeline at $3; that path is gone.)
    if (( $# >= 2 )) && [[ "$1" == "wing" ]]; then
        while IFS= read -r line; do
            _sw_absorb_flag_line
        done < <(sparkwing _complete-pipeline-flags "$2" 2>/dev/null)
    elif (( $# >= 2 )) && [[ "$1" == "run" ]]; then
        while IFS= read -r line; do
            _sw_absorb_flag_line
        done < <(sparkwing _complete-pipeline-flags "$2" 2>/dev/null)
    fi

    while IFS= read -r line; do
        _sw_absorb_flag_line
    done < <(sparkwing _complete-flags "$@" 2>/dev/null)

    local g
    local -a gnames gdescs
    for g in "${_sw_group_order[@]}"; do
        # Split the \n-joined buffers back into arrays. ${(f)...} on a
        # trailing-newline string yields an empty tail element, so we
        # drop it with the "(@)" parameter flag not-being-available
        # workaround: slice off the trailing entry when it's empty.
        gnames=( "${(@f)_sw_group_names[$g]}" )
        gdescs=( "${(@f)_sw_group_descs[$g]}" )
        # Drop the trailing empty element that ${(@f)...} leaves
        # behind when the source string ends in \n. Using [[ ]] here
        # because -z is a string test, not an arithmetic operator --
        # inside (( )) it never fires and the empty element leaks
        # into compadd, which some zsh configs render as a blank
        # selectable row (or silently drop the whole group).
        (( ${#gnames[@]} > 0 )) && [[ -z "${gnames[-1]}" ]] && gnames=( "${gnames[@]:0:-1}" )
        (( ${#gdescs[@]} > 0 )) && [[ -z "${gdescs[-1]}" ]] && gdescs=( "${gdescs[@]:0:-1}" )
        (( ${#gnames[@]} == 0 )) && continue
        # Sanitize group name into a zsh tag (spaces -> dashes, lower).
        local tag="${(L)g// /-}"
        # Bold + colored group header with a leading glyph so sections
        # are visually distinct in the completion menu. "Pipeline Args"
        # gets cyan (the group operators interact with most often);
        # everything else gets a muted magenta. The %B/%F/%f/%b prompt
        # escapes only render after the zstyle ':completion:*'
        # descriptions format we set at source-time (see the top of
        # this script); without that, compadd -X prints the raw
        # explanation untouched.
        local header_color="magenta"
        [[ "$g" == "Pipeline Args" || "$g" == "Script Args" ]] && header_color="cyan"
        compadd -l -X "%F{${header_color}}%B▸ ${g}%b%f" -J "$tag" -d gdescs -a gnames
    done
}

# _sparkwing_complete_pipelines is shared by 'sparkwing run <TAB>'
# and 'wing <TAB>'. Entries come back tab-separated as four
# columns -- name, group, kind, desc -- where kind is the
# pipeline-trigger model (currently always "pipeline"; the
# column persists for backward compat with the helper output).
# Bucket by group in first-seen order and render each row as:
# name-padded desc.
_sparkwing_complete_pipelines() {
    local -A _sw_p_names _sw_p_raw _sw_p_displays
    local -a _sw_p_order
    local line name group kind desc
    # First pass: collect rows and find the widest name across all
    # entries. One global width keeps every section aligned to the
    # same column (no staggering between groups). Capped at 24 so a
    # single pathologically-long name doesn't starve the desc column.
    local _sw_name_width=0
    local _sw_name_cap=30
    while IFS= read -r line; do
        name="${line%%$'\t'*}"
        line="${line#*$'\t'}"
        group="${line%%$'\t'*}"
        line="${line#*$'\t'}"
        kind="${line%%$'\t'*}"
        desc="${line#*$'\t'}"
        [[ -z "$group" ]] && group="Pipelines"
        if [[ -z "${_sw_p_names[$group]-}" ]]; then
            _sw_p_order+=("$group")
            _sw_p_names[$group]=""
            _sw_p_raw[$group]=""
            _sw_p_displays[$group]=""
        fi
        _sw_p_names[$group]+="$name"$'\n'
        # Stash raw row for the render pass using unit-separator
        # bytes -- never appear in names/descs in practice.
        _sw_p_raw[$group]+="$name"$'\x1f'"$kind"$'\x1f'"$desc"$'\n'
        local _sw_len=${#name}
        (( _sw_len > _sw_name_cap )) && _sw_len=$_sw_name_cap
        (( _sw_len > _sw_name_width )) && _sw_name_width=$_sw_len
    done < <(sparkwing _complete-pipelines 2>/dev/null)

    # Second pass: render every row at the single global width.
    local g nm k d col padded padded_name label
    for g in "${_sw_p_order[@]}"; do
        local raw="${_sw_p_raw[$g]}"
        local -a rows=( "${(@f)raw}" )
        (( ${#rows[@]} > 0 )) && [[ -z "${rows[-1]}" ]] && rows=( "${rows[@]:0:-1}" )
        for row in "${rows[@]}"; do
            nm="${row%%$'\x1f'*}"
            row="${row#*$'\x1f'}"
            k="${row%%$'\x1f'*}"
            d="${row#*$'\x1f'}"

            # Pad the bracketed kind to a fixed visible width for
            # column alignment. ANSI escapes would color these nicely
            # but zsh's completion engine miscounts their width when
            # the menu has to redraw (small terminals + tab cycling),
            # which leaves duplicated rows in the scrollback. Plain
            # text is reliable; color can come back later via
            # zstyle list-colors once the patterns are worked out.
            col="${(r:10:: :)${:-[$k]}}"

            if (( ${#nm} >= _sw_name_width )); then
                padded_name="$nm"
            else
                padded_name="${(r:$_sw_name_width:: :)nm}"
            fi
            if [[ -z "$d" || "$d" == "$nm" ]]; then
                label="$padded_name  $col"
            else
                # Indent = name_width + 2 + 10 + 2
                local _sw_indent=$(( _sw_name_width + 14 ))
                local _sw_max=$(( ${COLUMNS:-80} - _sw_indent - 1 ))
                if (( _sw_max > 10 )) && (( ${#d} > _sw_max )); then
                    d="${d[1,_sw_max-1]}…"
                fi
                label="$padded_name  $col  $d"
            fi
            _sw_p_displays[$g]+="$label"$'\n'
        done
    done

    # Third pass: feed each group to compadd. Do not redeclare g
    # here -- it was already declared above, and typeset g (which
    # local g expands to) with no value and an existing local
    # prints g=<current value> to stdout, which corrupts completion.
    # Re-use the existing local.
    local -a gnames gdisps
    for g in "${_sw_p_order[@]}"; do
        gnames=( "${(@f)_sw_p_names[$g]}" )
        gdisps=( "${(@f)_sw_p_displays[$g]}" )
        (( ${#gnames[@]} > 0 )) && [[ -z "${gnames[-1]}" ]] && gnames=( "${gnames[@]:0:-1}" )
        (( ${#gdisps[@]} > 0 )) && [[ -z "${gdisps[-1]}" ]] && gdisps=( "${gdisps[@]:0:-1}" )
        (( ${#gnames[@]} == 0 )) && continue
        local tag="${(L)g// /-}"
        # Both Pipelines and Scripts render in magenta; cyan is
        # reserved for the "Pipeline Args" / "Script Args" flag
        # groups so operators visually distinguish "what can I run"
        # from "what flags apply".
        compadd -l -X "%F{magenta}%B▸ ${g}%b%f" -J "$tag" -d gdisps -a gnames
    done
}

# wing <TAB>: first positional is a pipeline name; everything else
# is forwarded to the user's pipeline binary so we don't try to
# complete it.
_wing() {
    # 'wing -<TAB>' (flag at position 1, before any pipeline name)
    # -> wing-owned flags. Kept first so the '-' prefix wins over
    # the pipeline-name completer (which would match nothing and
    # let zsh fall through to unpredictable default behavior).
    if [[ $CURRENT -eq 2 && "${words[CURRENT]}" == -* ]]; then
        _sparkwing_complete_flags "wing"
        return
    fi
    # 'wing <TAB>' -> pipeline names.
    if [[ $CURRENT -eq 2 ]]; then
        _sparkwing_complete_pipelines
        return
    fi
    # 'wing <pipeline> --on <TAB>' -> profile names (matches the
    # sparkwing-level completion's --on handling). Kept first so
    # value-of-flag completion wins over generic flag listing.
    if [[ ${CURRENT} -ge 3 && "${words[CURRENT-1]}" == "--on" ]]; then
        local -a profs
        profs=( ${(f)"$(sparkwing _complete-profiles 2>/dev/null)"} )
        _describe -t profiles 'profile' profs
        return
    fi
    # Everything else after a pipeline name -> wing-owned flags
    # (--on/--from/--config/--help) merged with the pipeline's
    # typed flags from the describe cache. Offered on bare <TAB> as
    # well as '--<TAB>' so operators don't have to type '-' first.
    _sparkwing_complete_flags "wing" "${words[2]}"
}

compdef _sparkwing sparkwing
compdef _wing wing
`)
	return b.String()
}

func renderFish() string {
	var b strings.Builder
	b.WriteString(`# sparkwing fish completion
# Usage: sparkwing completion fish | source
#   (or write to ~/.config/fish/completions/sparkwing.fish)

function __sparkwing_profiles
    sparkwing _complete-profiles 2>/dev/null
end

function __sparkwing_pipelines
    # Columns: name, group, desc. Fish wants name\tdesc, so skip col 2.
    sparkwing _complete-pipelines 2>/dev/null | awk -F '\t' '{print $1"\t"$3}'
end

function __sparkwing_has_path
    # True if the current (unfinished) token stream matches the
    # given sequence of words exactly. Used to gate flag completions
    # to a specific leaf command.
    set -l tokens (commandline -opc)
    set -l want $argv
    if test (count $tokens) -lt (math (count $want) + 1)
        return 1
    end
    for i in (seq 1 (count $want))
        if test "$tokens[(math $i + 1)]" != "$want[$i]"
            return 1
        end
    end
    return 0
end

# Top-level subcommands.
`)
	for _, s := range topLevelSubcommands() {
		fmt.Fprintf(&b,
			"complete -c sparkwing -f -n 'not __fish_seen_subcommand_from %s' -a %q -d %q\n",
			joinSubcommandNames(topLevelSubcommands()), s.Name, s.Synopsis)
	}
	b.WriteString("\n# Verbs under parent commands.\n")
	for _, parent := range parentNames() {
		parentCmd := parentCommands()[parent]
		for _, s := range completableSubcommands(parentCmd) {
			fmt.Fprintf(&b,
				"complete -c sparkwing -f -n '__fish_seen_subcommand_from %s' -a %q -d %q\n",
				parent, s.Name, s.Synopsis)
		}
	}
	b.WriteString("\n# Pipeline names for `sparkwing run`.\n")
	b.WriteString(`complete -c sparkwing -f -n '__sparkwing_has_path run' -a '(__sparkwing_pipelines)'
`)
	b.WriteString("\n# Flags per leaf, pulled from the registry.\n")

	// Emit flags for every leaf, gated on the argv path.
	// Sort keys for stable output.
	leaves := leafCommands()
	keys := make([]string, 0, len(leaves))
	for k := range leaves {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		cmd := leaves[k]
		flags := append([]FlagSpec(nil), cmd.Flags...)
		if !hasFlagNamed(flags, "help") {
			flags = append(flags, helpFlag)
		}
		for _, f := range flags {
			desc := f.Desc
			switch {
			case f.Required:
				desc = "[required] " + desc
			case f.RequiredWhen != "":
				desc = "[conditional] " + desc
			}
			// Fish long-flag uses -l; short via -s. Value flags
			// (Argument != "") take -r so fish suggests an arg.
			line := fmt.Sprintf(
				"complete -c sparkwing -n '__sparkwing_has_path %s' -l %s",
				k, f.Name)
			if f.Short != "" {
				line += " -s " + f.Short
			}
			if f.Argument != "" {
				line += " -r"
			}
			line += fmt.Sprintf(" -d %q", desc)
			b.WriteString(line + "\n")
		}
	}

	// `wing` gets pipeline completion on its first positional.
	b.WriteString("\n# wing binary: first positional is a pipeline name.\n")
	b.WriteString(`complete -c wing -f -n 'not __fish_seen_subcommand_from (sparkwing _complete-pipelines 2>/dev/null | awk -F "\\t" "{print \\$1}")' -a '(__sparkwing_pipelines)'
`)
	return b.String()
}

func joinSubcommandNames(subs []SubcommandRef) string {
	parts := make([]string, len(subs))
	for i, s := range subs {
		parts[i] = s.Name
	}
	return strings.Join(parts, " ")
}
