package launcher

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/source"
)

// SourceInit publishes the bootstrap-authorized checkout for the Cloud host container.
type SourceInit struct {
	Bind func(context.Context, CloudJobAward) (source.Options, func() error, error)
}

// Run acquires the sealed commit without exchanging the bootstrap or executing repository code.
func (s SourceInit) Run(ctx context.Context, award CloudJobAward, destination string) (err error) {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if err := validateAward(award); err != nil {
		return err
	}
	if s.Bind == nil || !filepath.IsAbs(destination) || filepath.Clean(destination) == string(filepath.Separator) {
		return errors.New("Cloud source requires a source-access binding and absolute checkout destination")
	}
	destination = filepath.Clean(destination)
	if award.Source.LocalCheckout != "" || award.Source.RemoteURL == "" || award.Source.SHA == "" || award.Source.RepoKey == "" {
		return errors.New("Cloud source requires the sealed remote repository and commit")
	}
	if award.Binding.Kind != CloudPlan && (award.Source.Engine == nil || *award.Source.Engine != *award.Engine) {
		return errors.New("Cloud source differs from the accepted engine")
	}
	captured, err := captureAward(award)
	if err != nil {
		return err
	}
	deadline, err := cloudDeadline(time.Now(), captured.Binding)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	defer func() { err = errors.Join(err, context.Cause(ctx)) }()
	if err := absentCheckout(destination); err != nil {
		return err
	}
	options, closeBinding, err := s.Bind(ctx, award)
	defer func() {
		if closeBinding != nil {
			err = errors.Join(err, closeBinding())
		}
	}()
	if err != nil {
		return err
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	options.Workspace = filepath.Dir(destination)
	entry, err := source.PrepareCheckout(ctx, captured.Source, options)
	if entry != nil {
		defer func() { err = errors.Join(err, entry.Close()) }()
	}
	if err != nil {
		return err
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if entry == nil || entry.RepoKey != captured.Source.RepoKey || !strings.EqualFold(entry.SHA, captured.Source.SHA) {
		return errors.New("Cloud checkout differs from the sealed source")
	}
	if err := absentCheckout(destination); err != nil {
		return err
	}
	return os.Rename(entry.CheckoutDirectory(), destination)
}

func absentCheckout(path string) error {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("Cloud checkout destination already exists")
}
