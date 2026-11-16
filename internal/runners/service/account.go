package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Account is the dedicated OS user a system runner service runs as.
type Account struct {
	Name string `json:"name"`
	UID  int    `json:"uid"`
	GID  int    `json:"gid"`
	Home string `json:"home"`
}

// AccountHost reaches the machine's user database through native commands.
type AccountHost struct {
	GOOS string
	// HomeRoot holds the homes of accounts this package creates.
	HomeRoot string
	// InvokerName and InvokerUID name the person who asked for the service, so
	// their own account is never reused; InvokerUID is -1 when unknown.
	InvokerName string
	InvokerUID  int
	// PeopleRoots hold people's homes; nil means /home, /Users and /root.
	PeopleRoots []string
	Exec        func(context.Context, string, ...string) (string, error)
	Chown       func(path string, uid, gid int) error
}

// DefaultHomeRoot is where created service accounts live.
func DefaultHomeRoot(goos string) string {
	if goos == "darwin" {
		return "/private/var/sparkwing"
	}
	return "/var/lib/sparkwing"
}

var accountName = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,30}$`)

// ValidAccountName refuses names the native user tools would reject or misread.
func ValidAccountName(name string) error {
	if !accountName.MatchString(name) {
		return fmt.Errorf("service user %q must be 1-31 lowercase letters, digits, '-' or '_', starting with a letter or '_'", name)
	}
	return nil
}

var peopleRoots = []string{"/home", "/Users", "/root"}

func underAny(path string, roots []string) bool {
	for _, root := range roots {
		if path == root || strings.HasPrefix(path, root+"/") {
			return true
		}
	}
	return false
}

// safety: membership in any of these lets a job act as root, so a service account must hold none.
var privilegedGroups = []string{"root", "sudo", "wheel", "admin", "docker", "lxd"}

// LookupAccount reads an existing account, reporting false when it is absent.
func LookupAccount(ctx context.Context, h AccountHost, name string) (Account, bool, error) {
	if err := ValidAccountName(name); err != nil {
		return Account{}, false, err
	}
	switch h.GOOS {
	case "linux":
		out, err := h.run(ctx, "getent", "passwd", name)
		if err != nil {
			if strings.TrimSpace(out) == "" {
				return Account{}, false, nil
			}
			return Account{}, false, fmt.Errorf("service user: getent passwd %s: %w: %s", name, err, strings.TrimSpace(out))
		}
		fields := strings.Split(strings.TrimSpace(out), ":")
		if len(fields) < 7 || fields[0] != name {
			return Account{}, false, fmt.Errorf("service user: unreadable passwd entry for %s", name)
		}
		uid, uidErr := strconv.Atoi(fields[2])
		gid, gidErr := strconv.Atoi(fields[3])
		if err := errors.Join(uidErr, gidErr); err != nil {
			return Account{}, false, fmt.Errorf("service user: passwd entry for %s: %w", name, err)
		}
		return Account{Name: name, UID: uid, GID: gid, Home: fields[5]}, true, nil
	case "darwin":
		out, err := h.run(ctx, "dscl", ".", "-read", "/Users/"+name, "UniqueID", "PrimaryGroupID", "NFSHomeDirectory")
		if err != nil {
			if strings.Contains(out, "eDSRecordNotFound") {
				return Account{}, false, nil
			}
			return Account{}, false, fmt.Errorf("service user: dscl read %s: %w: %s", name, err, strings.TrimSpace(out))
		}
		values := map[string]string{}
		for _, line := range strings.Split(out, "\n") {
			if key, value, ok := strings.Cut(line, ":"); ok {
				values[strings.TrimSpace(key)] = strings.TrimSpace(value)
			}
		}
		uid, uidErr := strconv.Atoi(values["UniqueID"])
		gid, gidErr := strconv.Atoi(values["PrimaryGroupID"])
		if err := errors.Join(uidErr, gidErr); err != nil {
			return Account{}, false, fmt.Errorf("service user: directory record for %s: %w", name, err)
		}
		return Account{Name: name, UID: uid, GID: gid, Home: values["NFSHomeDirectory"]}, true, nil
	default:
		return Account{}, false, unsupported(h.GOOS)
	}
}

// EnsureAccount reuses a suitable account or creates an unprivileged one with
// no password, no login shell and a private home under HomeRoot.
func EnsureAccount(ctx context.Context, h AccountHost, name string) (Account, bool, error) {
	account, found, err := LookupAccount(ctx, h, name)
	if err != nil {
		return Account{}, false, err
	}
	created := false
	if !found {
		if account, err = createAccount(ctx, h, name); err != nil {
			return Account{}, false, err
		}
		created = true
	}
	return account, created, checkAccount(ctx, h, account)
}

func createAccount(ctx context.Context, h AccountHost, name string) (Account, error) {
	if !filepath.IsAbs(h.HomeRoot) {
		return Account{}, errors.New("service user: HomeRoot must be an absolute path")
	}
	home := filepath.Join(h.HomeRoot, name)
	if err := os.MkdirAll(h.HomeRoot, 0o755); err != nil {
		return Account{}, fmt.Errorf("service user: create %s: %w", h.HomeRoot, err)
	}
	switch h.GOOS {
	case "linux":
		if err := runCommandArgs(ctx, h.run, "useradd", "--system", "--user-group", "--create-home", "--home-dir", home, "--shell", "/usr/sbin/nologin", "--comment", "Sparkwing runner", name); err != nil {
			return Account{}, err
		}
	case "darwin":
		id, err := freeDarwinID(ctx, h)
		if err != nil {
			return Account{}, err
		}
		value := strconv.Itoa(id)
		group, user := "/Groups/"+name, "/Users/"+name
		for _, args := range [][]string{
			{group},
			{group, "PrimaryGroupID", value},
			{group, "Password", "*"},
			{user},
			{user, "UniqueID", value},
			{user, "PrimaryGroupID", value},
			{user, "UserShell", "/usr/bin/false"},
			{user, "NFSHomeDirectory", home},
			{user, "RealName", "Sparkwing runner"},
			{user, "Password", "*"},
			{user, "IsHidden", "1"},
		} {
			if err := runCommandArgs(ctx, h.run, "dscl", append([]string{".", "-create"}, args...)...); err != nil {
				return Account{}, err
			}
		}
		if err := os.MkdirAll(home, 0o700); err != nil {
			return Account{}, fmt.Errorf("service user: create %s: %w", home, err)
		}
		if err := h.chown(home, id, id); err != nil {
			return Account{}, err
		}
	default:
		return Account{}, unsupported(h.GOOS)
	}
	account, found, err := LookupAccount(ctx, h, name)
	if err == nil && !found {
		err = fmt.Errorf("service user: %s is missing after it was created", name)
	}
	if err != nil {
		return Account{}, err
	}
	// safety: useradd applies the login.defs home mode, which may let other accounts read it.
	if err := os.Chmod(account.Home, 0o700); err != nil {
		return Account{}, fmt.Errorf("service user: make %s private: %w", account.Home, err)
	}
	return account, nil
}

func freeDarwinID(ctx context.Context, h AccountHost) (int, error) {
	used := map[int]bool{}
	for _, list := range [][]string{{"/Users", "UniqueID"}, {"/Groups", "PrimaryGroupID"}} {
		out, err := h.run(ctx, "dscl", ".", "-list", list[0], list[1])
		if err != nil {
			return 0, fmt.Errorf("service user: dscl list %s: %w: %s", list[0], err, strings.TrimSpace(out))
		}
		for _, line := range strings.Split(out, "\n") {
			fields := strings.Fields(line)
			if len(fields) == 2 {
				if id, err := strconv.Atoi(fields[1]); err == nil {
					used[id] = true
				}
			}
		}
	}
	for id := 400; id < 500; id++ {
		if !used[id] {
			return id, nil
		}
	}
	return 0, errors.New("service user: no free user and group id between 400 and 499")
}

func checkAccount(ctx context.Context, h AccountHost, a Account) error {
	refuse := func(reason string) error {
		return fmt.Errorf("service user %s cannot run a shared runner: %s; choose another name and sparkwing creates a dedicated account", a.Name, reason)
	}
	if a.UID == 0 {
		return refuse("it is root")
	}
	if a.Name == h.InvokerName || (h.InvokerUID >= 0 && a.UID == h.InvokerUID) {
		return refuse("it is your own account")
	}
	clean := filepath.Clean(a.Home)
	if !filepath.IsAbs(a.Home) || clean == "/" {
		return refuse(fmt.Sprintf("its home %q is not a private directory", a.Home))
	}
	roots := h.PeopleRoots
	if roots == nil {
		roots = peopleRoots
	}
	if underAny(clean, roots) {
		return refuse(fmt.Sprintf("its home %s sits with people's homes, which the service hides from jobs", a.Home))
	}
	out, err := h.run(ctx, "id", "-Gn", a.Name)
	if err != nil {
		return fmt.Errorf("service user: id -Gn %s: %w: %s", a.Name, err, strings.TrimSpace(out))
	}
	for _, group := range strings.Fields(out) {
		if slices.Contains(privilegedGroups, group) {
			return refuse(fmt.Sprintf("it belongs to the %s group, which can act as root", group))
		}
	}
	info, err := os.Lstat(a.Home)
	if err != nil {
		return refuse(fmt.Sprintf("its home %s is unreadable: %v", a.Home, err))
	}
	if !info.IsDir() {
		return refuse(fmt.Sprintf("its home %s is not a directory", a.Home))
	}
	if owner, ok := fileOwner(info); ok && owner != a.UID {
		return refuse(fmt.Sprintf("its home %s belongs to uid %d", a.Home, owner))
	}
	return nil
}

func (h AccountHost) run(ctx context.Context, name string, args ...string) (string, error) {
	if h.Exec == nil {
		return "", errors.New("service user: AccountHost.Exec is required")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return h.Exec(ctx, name, args...)
}

func (h AccountHost) chown(path string, uid, gid int) error {
	chown := h.Chown
	if chown == nil {
		chown = os.Chown
	}
	if err := chown(path, uid, gid); err != nil {
		return fmt.Errorf("service user: give %s to uid %d: %w", path, uid, err)
	}
	return nil
}

func runCommandArgs(ctx context.Context, run func(context.Context, string, ...string) (string, error), name string, args ...string) error {
	out, err := run(ctx, name, args...)
	if err != nil {
		return fmt.Errorf("service user: %s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(out))
	}
	return nil
}
