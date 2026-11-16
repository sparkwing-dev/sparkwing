package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type landCheckRequirements struct {
	RunID    string   `json:"run_id"`
	Required []string `json:"required"`
}

const queuedCheckRemedy = "enqueue again through main's bin/queue-land.sh"

type landCheckReceipt struct {
	RunID   string `json:"run_id"`
	Check   string `json:"check"`
	Tree    string `json:"tree"`
	Success bool   `json:"success"`
}

type queuedChecks struct {
	Owner    string
	RunID    string
	Head     sparkwing.Ref[landHead]
	required map[string]bool
	once     sync.Once
	enterErr error
}

func (q *queuedChecks) names() []string {
	var names []string
	for key := range q.required {
		names = append(names, key)
	}
	sort.Strings(names)
	return names
}

func checkedValue[T any](q *queuedChecks, key string, head sparkwing.Ref[landHead], fn func(context.Context) (T, error)) func(context.Context) (T, error) {
	if q == nil {
		return fn
	}
	if q.required == nil {
		q.required = map[string]bool{}
	}
	q.required[key] = true
	return func(ctx context.Context) (out T, err error) {
		err = q.run(ctx, key, head.Get(ctx), func(ctx context.Context) error { out, err = fn(ctx); return err })
		return out, err
	}
}

func (q *queuedChecks) wrap(key string, head sparkwing.Ref[landHead], fn func(context.Context) error) func(context.Context) error {
	wrapped := checkedValue(q, key, head, func(ctx context.Context) (struct{}, error) { return struct{}{}, fn(ctx) })
	return func(ctx context.Context) error { _, err := wrapped(ctx); return err }
}

// safety: each native SDK process hosts one job. All its authorizing bodies wait
// here before their parallel work starts; planning itself never changes directories.
func (q *queuedChecks) enter() error {
	q.once.Do(func() {
		if !filepath.IsAbs(q.Owner) {
			q.enterErr = errors.New("queued checks require the absolute owner checkout")
			return
		}
		if !isSourceTreeRoot(q.Owner) {
			q.enterErr = errors.New("queued owner checkout is missing its project markers")
			return
		}
		q.enterErr = os.Chdir(q.Owner)
	})
	return q.enterErr
}

func (q *queuedChecks) inspect(ctx context.Context, head landHead) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	owner, err := os.Stat(q.Owner)
	if err != nil {
		return "", err
	}
	here, err := os.Stat(cwd)
	if err != nil {
		return "", err
	}
	if !os.SameFile(owner, here) {
		return "", fmt.Errorf("queued check is outside the merged owner checkout: %s", cwd)
	}
	git := gitIn(cwd)
	if err := verifyLandingCheckout(ctx, git, cwd, head); err != nil {
		return "", err
	}
	tree, err := git(ctx, "rev-parse", "HEAD^{tree}")
	if err == nil && tree != head.Tree {
		err = fmt.Errorf("queued check tree %s differs from merged owner tree %s", tree, head.Tree)
	}
	return tree, err
}

func (q *queuedChecks) run(ctx context.Context, key string, head landHead, fn func(context.Context) error) error {
	if head.Checks == nil {
		return fmt.Errorf("queued check %q: preconditions receipt set is missing for run %q; %s", key, q.RunID, queuedCheckRemedy)
	}
	if head.Checks.RunID != q.RunID {
		return fmt.Errorf("queued check %q: preconditions run %q differs from executing run %q; %s", key, head.Checks.RunID, q.RunID, queuedCheckRemedy)
	}
	if len(head.Checks.Required) == 0 {
		return fmt.Errorf("queued check %q: preconditions receipt set is empty for run %q; %s", key, q.RunID, queuedCheckRemedy)
	}
	if err := q.enter(); err != nil {
		return fmt.Errorf("bind queued check to owner: %w", err)
	}
	tree, err := q.inspect(ctx, head)
	if err != nil {
		return err
	}
	err = fn(ctx)
	_, afterErr := q.inspect(ctx, head)
	err = errors.Join(err, afterErr)
	receipt := landCheckReceipt{RunID: q.RunID, Check: key, Tree: tree, Success: err == nil}
	common, commonErr := gitIn(q.Owner)(ctx, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if commonErr != nil {
		return errors.Join(err, commonErr)
	}
	file, pathErr := landCheckPath(common, q.RunID, key)
	if pathErr != nil {
		return errors.Join(err, pathErr)
	}
	if writeErr := writeLandCheckReceipt(file, receipt); writeErr != nil {
		return errors.Join(err, writeErr)
	}
	sparkwing.Info(ctx, "landing check: run=%s check=%s tree=%s success=%t", q.RunID, key, tree, receipt.Success)
	return err
}

var landCheckToken = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func landCheckPath(common, run, key string) (string, error) {
	if !landCheckToken.MatchString(run) || filepath.IsAbs(key) || path.Clean(key) != key || key == "." || key == ".." {
		return "", errors.New("invalid queued check identity")
	}
	// safety: check keys come from declared steps, but receipts must never escape their run directory.
	for _, part := range strings.Split(key, "/") {
		if !landCheckToken.MatchString(part) {
			return "", errors.New("invalid queued check key")
		}
	}
	return filepath.Join(common, "sparkwing-land-checks", run, filepath.FromSlash(key)+".json"), nil
}

func writeLandCheckReceipt(file string, receipt landCheckReceipt) (err error) {
	data, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	staged, err := os.CreateTemp(filepath.Dir(file), ".receipt-")
	if err != nil {
		return err
	}
	defer func() {
		if cleanupErr := os.Remove(staged.Name()); cleanupErr != nil && !os.IsNotExist(cleanupErr) {
			err = errors.Join(err, cleanupErr)
		}
	}()
	if _, err := staged.Write(data); err != nil {
		return errors.Join(err, staged.Close())
	}
	if err := staged.Close(); err != nil {
		return err
	}
	return os.Rename(staged.Name(), file)
}

func verifyLandChecks(ctx context.Context, git gitRunner, head landHead) error {
	if head.Checks == nil {
		return nil
	}
	if head.Checks.RunID == "" || len(head.Checks.Required) == 0 {
		return errors.New("land: authorizing check receipts are missing")
	}
	common, err := git(ctx, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return err
	}
	for _, key := range head.Checks.Required {
		file, err := landCheckPath(common, head.Checks.RunID, key)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("land: missing authorizing check %s; %s: %w", key, queuedCheckRemedy, err)
		}
		var receipt landCheckReceipt
		if err := json.Unmarshal(data, &receipt); err != nil {
			return fmt.Errorf("land: unreadable authorizing check %s: %w", key, err)
		}
		if receipt.RunID != head.Checks.RunID || receipt.Check != key {
			return fmt.Errorf("land: check %q identity mismatch: expected run %q check %q, got run %q check %q; %s", key, head.Checks.RunID, key, receipt.RunID, receipt.Check, queuedCheckRemedy)
		}
		if !receipt.Success {
			return fmt.Errorf("land: authorizing check %s did not pass", key)
		}
		if receipt.Tree != head.Tree {
			return fmt.Errorf("land: check %s recorded checked tree %s, pushing %s (success=%t); %s", key, receipt.Tree, head.Tree, receipt.Success, queuedCheckRemedy)
		}
	}
	return nil
}
