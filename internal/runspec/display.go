package runspec

import "sort"

func validClassification(names []string, args ...map[string]string) bool {
	if names == nil {
		return false
	}
	seen := map[string]bool{}
	for _, name := range names {
		if name == "" || seen[name] {
			return false
		}
		seen[name] = true
		found := false
		for _, values := range args {
			if _, ok := values[name]; ok {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func redactArgs(args map[string]string, names []string, classified bool) map[string]string {
	out := make(map[string]string, len(args))
	secrets := map[string]bool{}
	for _, name := range names {
		secrets[name] = true
	}
	for key, value := range args {
		if !classified || secrets[key] {
			value = RedactedArgValue
		}
		out[key] = value
	}
	return out
}

func displayBase(r Request) Display {
	git := r.Git
	git.Repository.Display = safeDisplay(git.Repository.Display)
	return Display{Pipeline: r.Pipeline, Git: git, Actor: r.Actor, PullRequest: r.PullRequest, Lineage: r.Lineage, EnvNames: append([]string(nil), r.Env.Names...)}
}

// DisplayRequest hides all values until describe supplied a valid classification.
func DisplayRequest(s SealedRequest) Display {
	r := s.Request
	d := displayBase(r)
	classified := r.SecretArgs.Set && validClassification(r.SecretArgs.Value, r.RequestedArgs)
	d.RequestedArgs = redactArgs(r.RequestedArgs, r.SecretArgs.Value, classified)
	if classified {
		d.SecretArgs = append([]string{}, r.SecretArgs.Value...)
	}
	return d
}

// DisplaySpec strips private hashes and masks requested and resolved secret arguments alike.
func DisplaySpec(s Spec) Display {
	d := displayBase(s.Request.Request)
	classified := validClassification(s.Resolved.SecretArgs, s.Resolved.Args, s.Request.Request.RequestedArgs)
	d.RequestedArgs = redactArgs(s.Request.Request.RequestedArgs, s.Resolved.SecretArgs, classified)
	d.Args = redactArgs(s.Resolved.Args, s.Resolved.SecretArgs, classified)
	if classified {
		d.SecretArgs = append([]string{}, s.Resolved.SecretArgs...)
	}
	if s.Resolved.DefaultBranch.Set {
		d.Git.DefaultBranch = s.Resolved.DefaultBranch
	}
	d.EnvNames = append([]string(nil), s.Resolved.Env.Names...)
	d.Reproducer = s.Reproducer()
	return d
}

// Reproducer emits argv, avoiding shell interpretation and every secret argument value.
func (s Spec) Reproducer() []string {
	argv := []string{"sparkwing", "run", s.Request.Request.Pipeline}
	if !validClassification(s.Resolved.SecretArgs, s.Resolved.Args, s.Request.Request.RequestedArgs) {
		return argv
	}
	secrets := map[string]bool{}
	for _, name := range s.Resolved.SecretArgs {
		secrets[name] = true
	}
	names := make([]string, 0, len(s.Resolved.Args))
	for name := range s.Resolved.Args {
		if !secrets[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) > 0 {
		argv = append(argv, "--")
	}
	for _, name := range names {
		argv = append(argv, "--"+name+"="+s.Resolved.Args[name])
	}
	return argv
}
