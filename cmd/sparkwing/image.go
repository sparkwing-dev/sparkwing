package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	flag "github.com/spf13/pflag"
	"go.yaml.in/yaml/v3"
)

func runImage(args []string) error {
	if handleParentHelp(cmdImage, args) {
		return nil
	}
	if len(args) == 0 {
		PrintHelp(cmdImage, os.Stderr)
		return errors.New("image: subcommand required (rollout)")
	}
	switch args[0] {
	case "rollout":
		return runImageRollout(args[1:])
	default:
		PrintHelp(cmdImage, os.Stderr)
		return fmt.Errorf("image: unknown subcommand %q", args[0])
	}
}

func runImageRollout(args []string) error {
	fs := flag.NewFlagSet(cmdImageRollout.Path, flag.ContinueOnError)
	image := fs.String("image", "", "short image name (matches suffix of ECR URL)")
	tag := fs.String("tag", "", "new tag to write")
	gitopsRepo := fs.String("gitops-repo", "", "gitops repo path (or SPARKWING_GITOPS_REPO)")
	namespace := fs.String("namespace", "sparkwing", "kubernetes namespace for rollout + logs")
	argocdApp := fs.String("argocd-app", "", "argocd app name (default: derived from --image)")
	message := fs.String("message", "", "override the commit message")
	wait := fs.Bool("wait", false, "block until kubectl rollout status completes")
	tailLogs := fs.Bool("tail-logs", false, "tail pod logs after rollout completes")
	dryRun := fs.Bool("dry-run", false, "print the plan without writing, committing, pushing, or syncing")
	if err := parseAndCheck(cmdImageRollout, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}

	repoRoot, err := resolveGitopsRepo(*gitopsRepo)
	if err != nil {
		return fmt.Errorf("image rollout: %w", err)
	}

	kustPath := filepath.Join(repoRoot, "sparkwing", "kustomization.yaml")
	if _, err := os.Stat(kustPath); err != nil {
		return fmt.Errorf("image rollout: %w", err)
	}

	fmt.Fprintf(os.Stdout, "plan:\n")
	fmt.Fprintf(os.Stdout, "  gitops repo : %s\n", repoRoot)
	fmt.Fprintf(os.Stdout, "  kustomize   : %s\n", kustPath)
	fmt.Fprintf(os.Stdout, "  image       : %s\n", *image)
	fmt.Fprintf(os.Stdout, "  new tag     : %s\n", *tag)

	matchedURL, currentTag, err := findImageEntry(kustPath, *image)
	if err != nil {
		return fmt.Errorf("image rollout: %w", err)
	}
	fmt.Fprintf(os.Stdout, "  matched     : %s (currently %s)\n", matchedURL, currentTag)

	if *dryRun {
		fmt.Fprintf(os.Stdout, "  [dry-run] would rewrite newTag -> %s\n", *tag)
		fmt.Fprintf(os.Stdout, "  [dry-run] would commit+push\n")
		planSyncAndWait(*image, *argocdApp, *namespace, *wait, *tailLogs)
		return nil
	}

	if currentTag == *tag {
		fmt.Fprintln(os.Stdout, "  newTag already matches; skipping rewrite")
	} else {
		if err := rewriteImageTag(kustPath, *image, *tag); err != nil {
			return fmt.Errorf("image rollout: %w", err)
		}
		fmt.Fprintf(os.Stdout, "  rewrote newTag -> %s\n", *tag)
	}

	commitMsg := *message
	if commitMsg == "" {
		commitMsg = fmt.Sprintf("chore: bump %s to %s", *image, *tag)
	}

	sha, committed, err := gitCommitAndPush(repoRoot, kustPath, commitMsg)
	if err != nil {
		return fmt.Errorf("image rollout: %w", err)
	}
	if committed {
		fmt.Fprintf(os.Stdout, "  committed   : %s\n", sha)
	} else {
		fmt.Fprintln(os.Stdout, "  committed   : (nothing to commit, tree clean)")
	}

	app := *argocdApp
	if app == "" {
		app = deriveArgoCDApp(*image)
	}
	if err := maybeArgoCDSync(app); err != nil {
		return fmt.Errorf("image rollout: %w", err)
	}

	deployName := deriveDeploymentName(*image)
	if *wait {
		if err := kubectlRolloutStatus(deployName, *namespace); err != nil {
			return fmt.Errorf("image rollout: %w", err)
		}
	}
	if *tailLogs {
		if err := kubectlTailLogs(deployName, *namespace); err != nil {
			return fmt.Errorf("image rollout: %w", err)
		}
	}
	return nil
}

func resolveGitopsRepo(explicit string) (string, error) {
	candidate := strings.TrimSpace(explicit)
	source := "--gitops-repo"
	if candidate == "" {
		candidate = strings.TrimSpace(os.Getenv("SPARKWING_GITOPS_REPO"))
		source = "SPARKWING_GITOPS_REPO"
	}
	if candidate == "" {
		return "", errors.New("gitops repo is not configured (pass --gitops-repo or set SPARKWING_GITOPS_REPO)")
	}
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", source, err)
	}
	return abs, nil
}

func findImageEntry(path, target string) (matchedName, currentTag string, err error) {
	data, rerr := os.ReadFile(path)
	if rerr != nil {
		return "", "", fmt.Errorf("read %s: %w", path, rerr)
	}
	var root yaml.Node
	if uerr := yaml.Unmarshal(data, &root); uerr != nil {
		return "", "", fmt.Errorf("parse %s: %w", path, uerr)
	}
	images, ierr := findImagesSeq(&root)
	if ierr != nil {
		return "", "", fmt.Errorf("%s: %w", path, ierr)
	}
	var matches []*yaml.Node
	var matchedNames []string
	for _, entry := range images.Content {
		if entry.Kind != yaml.MappingNode {
			continue
		}
		name := scalarField(entry, "name")
		if name == "" {
			continue
		}
		if imageNameMatches(name, target) {
			matches = append(matches, entry)
			matchedNames = append(matchedNames, name)
		}
	}
	switch len(matches) {
	case 0:
		return "", "", fmt.Errorf("no image entry matches %q", target)
	case 1:
		tag := scalarField(matches[0], "newTag")
		return matchedNames[0], tag, nil
	default:
		return "", "", fmt.Errorf("ambiguous --image %q: matches %v", target, matchedNames)
	}
}

func rewriteImageTag(path, target, newTag string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	images, err := findImagesSeq(&root)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	var matched *yaml.Node
	for _, entry := range images.Content {
		if entry.Kind != yaml.MappingNode {
			continue
		}
		name := scalarField(entry, "name")
		if imageNameMatches(name, target) {
			if matched != nil {
				return fmt.Errorf("ambiguous --image %q", target)
			}
			matched = entry
		}
	}
	if matched == nil {
		return fmt.Errorf("no image entry matches %q", target)
	}
	if err := setScalarField(matched, "newTag", newTag); err != nil {
		return fmt.Errorf("set newTag: %w", err)
	}
	var buf strings.Builder
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&root); err != nil {
		return fmt.Errorf("encode yaml: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("close encoder: %w", err)
	}
	if err := os.WriteFile(path, []byte(buf.String()), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func findImagesSeq(root *yaml.Node) (*yaml.Node, error) {
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return nil, errors.New("empty or malformed yaml document")
	}
	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		return nil, errors.New("top-level yaml is not a mapping")
	}
	for i := 0; i+1 < len(doc.Content); i += 2 {
		key := doc.Content[i]
		val := doc.Content[i+1]
		if key.Value == "images" {
			if val.Kind != yaml.SequenceNode {
				return nil, errors.New("images: is not a sequence")
			}
			return val, nil
		}
	}
	return nil, errors.New("no images: block found")
}

func scalarField(m *yaml.Node, key string) string {
	if m.Kind != yaml.MappingNode {
		return ""
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key && m.Content[i+1].Kind == yaml.ScalarNode {
			return m.Content[i+1].Value
		}
	}
	return ""
}

func setScalarField(m *yaml.Node, key, value string) error {
	if m.Kind != yaml.MappingNode {
		return errors.New("setScalarField: not a mapping")
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1].Value = value
			m.Content[i+1].Tag = "!!str"
			return nil
		}
	}
	m.Content = append(
		m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Value: value, Tag: "!!str"},
	)
	return nil
}

func imageNameMatches(fullName, target string) bool {
	if fullName == "" || target == "" {
		return false
	}
	if fullName == target {
		return true
	}
	return strings.HasSuffix(fullName, "/"+target)
}

func gitCommitAndPush(repoRoot, kustPath, message string) (sha string, committed bool, err error) {
	relPath, rerr := filepath.Rel(repoRoot, kustPath)
	if rerr != nil {
		return "", false, fmt.Errorf("relpath: %w", rerr)
	}
	if _, aerr := runGit(repoRoot, "add", relPath); aerr != nil {
		return "", false, fmt.Errorf("%w", aerr)
	}
	cmd := exec.Command("git", "-C", repoRoot, "diff", "--cached", "--quiet")
	if cerr := cmd.Run(); cerr == nil {
		out, herr := runGit(repoRoot, "rev-parse", "HEAD")
		if herr != nil {
			return "", false, fmt.Errorf("%w", herr)
		}
		return strings.TrimSpace(out), false, nil
	}
	if _, cerr := runGit(repoRoot, "commit", "-m", message); cerr != nil {
		return "", false, fmt.Errorf("%w", cerr)
	}
	shaOut, rerr := runGit(repoRoot, "rev-parse", "HEAD")
	if rerr != nil {
		return "", false, fmt.Errorf("%w", rerr)
	}
	sha = strings.TrimSpace(shaOut)
	if _, perr := runGit(repoRoot, "push"); perr != nil {
		return "", false, fmt.Errorf("%w", perr)
	}
	return sha, true, nil
}

func maybeArgoCDSync(app string) error {
	if _, err := exec.LookPath("argocd"); err != nil {
		fmt.Fprintln(os.Stdout, "  argocd not on PATH, skipping sync")
		return nil
	}
	fmt.Fprintf(os.Stdout, "  argocd sync : %s\n", app)
	cmd := exec.Command("argocd", "app", "sync", app)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("argocd app sync %s: %w", app, err)
	}
	return nil
}

func kubectlRolloutStatus(deploy, namespace string) error {
	if _, err := exec.LookPath("kubectl"); err != nil {
		return fmt.Errorf("kubectl not on PATH; --wait requires kubectl")
	}
	fmt.Fprintf(os.Stdout, "  kubectl rollout status deployment/%s -n %s\n", deploy, namespace)
	cmd := exec.Command("kubectl", "rollout", "status", "deployment/"+deploy, "-n", namespace)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("kubectl rollout status: %w", err)
	}
	return nil
}

func kubectlTailLogs(deploy, namespace string) error {
	if _, err := exec.LookPath("kubectl"); err != nil {
		return fmt.Errorf("kubectl not on PATH; --tail-logs requires kubectl")
	}
	fmt.Fprintf(os.Stdout, "  kubectl logs -f -l app=%s -n %s (ctrl-c to stop)\n", deploy, namespace)
	cmd := exec.Command("kubectl", "logs", "-f", "-l", "app="+deploy, "-n", namespace)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("kubectl logs: %w", err)
	}
	return nil
}

func deriveDeploymentName(image string) string {
	return image
}

func deriveArgoCDApp(image string) string {
	if strings.HasPrefix(image, "sparkwing") {
		return "sparkwing"
	}
	return image
}

func planSyncAndWait(image, argocdApp, namespace string, wait, tailLogs bool) {
	app := argocdApp
	if app == "" {
		app = deriveArgoCDApp(image)
	}
	if _, err := exec.LookPath("argocd"); err != nil {
		fmt.Fprintln(os.Stdout, "  [dry-run] argocd not on PATH; would skip sync")
	} else {
		fmt.Fprintf(os.Stdout, "  [dry-run] would argocd app sync %s\n", app)
	}
	if wait {
		fmt.Fprintf(os.Stdout, "  [dry-run] would kubectl rollout status deployment/%s -n %s\n",
			deriveDeploymentName(image), namespace)
	}
	if tailLogs {
		fmt.Fprintf(os.Stdout, "  [dry-run] would kubectl logs -f -l app=%s -n %s\n",
			deriveDeploymentName(image), namespace)
	}
}
