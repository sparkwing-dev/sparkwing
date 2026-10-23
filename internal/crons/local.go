package crons

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
	"github.com/sparkwing-dev/sparkwing/pkg/projectconfig"
)

// LocalRegistration binds a checkout's selected project to captured private inputs.
type LocalRegistration struct {
	Pipeline     string                `json:"pipeline"`
	ScheduleName string                `json:"schedule_name"`
	RepoPath     string                `json:"repo_path"`
	Project      *projectconfig.Config `json:"project"`
	Environment  runspec.EnvSnapshot   `json:"environment"`
}

var localScheduleID = regexp.MustCompile(`^crn_[a-f0-9]{12}$`)

// ReadLocalRegistration loads one schedule's host-owned intake inputs.
func ReadLocalRegistration(ctx context.Context, home, id string) (_ LocalRegistration, err error) {
	var result LocalRegistration
	if err := context.Cause(ctx); err != nil {
		return result, err
	}
	if !filepath.IsAbs(home) || !localScheduleID.MatchString(id) {
		return result, errors.New("invalid local schedule registration identity")
	}
	file, err := fssecure.OpenPrivateConfig(filepath.Join(home, "schedule-registrations", id+".json"))
	if err != nil {
		return result, errors.New("local schedule registration is unavailable")
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	data, err := io.ReadAll(file)
	if err != nil {
		return result, errors.New("local schedule registration is unreadable")
	}
	if json.Unmarshal(data, &result) != nil || !filepath.IsAbs(result.RepoPath) || result.Project == nil || result.Environment.Digest == "" || result.Pipeline == "" || scheduleID(result.RepoPath, result.Pipeline, result.ScheduleName) != id || projectconfig.Validate(*result.Project) != nil {
		return LocalRegistration{}, errors.New("local schedule registration is malformed")
	}
	return result, nil
}

// RegisterLocal uses the supplied declaration set without withdrawing unselected schedules.
func (s *Service) RegisterLocal(ctx context.Context, input controllerwire.LocalScheduleRegistration, prove Prover, capture func(context.Context, []string) (runspec.EnvSnapshot, error), commit func(context.Context, []store.CronSchedule, LocalRegistration, bool) (ArmReport, error)) (_ ArmReport, err error) {
	if s.Side != "local" || !filepath.IsAbs(input.RepoPath) || input.Project == nil || capture == nil || (input.Mode != controllerwire.ScheduleReplace && input.Mode != controllerwire.ScheduleUpsert) || (!input.Prove && !input.Follow) {
		return ArmReport{}, errors.New("invalid local schedule registration")
	}
	if err := projectconfig.Validate(*input.Project); err != nil {
		return ArmReport{}, err
	}
	root := filepath.Clean(input.RepoPath)
	available := map[string]Declared{}
	for _, pipeline := range input.Project.Registry().Pipelines {
		for _, entry := range pipeline.On.Schedule {
			if entry.Where == pipelines.ScheduleWhereLocal {
				d := Declared{RepoPath: root, Pipeline: pipeline.Name, Name: entry.EffectiveName(), Trigger: entry, Team: s.team()}
				available[d.selector()] = d
			}
		}
	}
	var selected []Declared
	seen := map[string]bool{}
	for _, entry := range input.Schedules {
		name := entry.Name
		if name == "" {
			name = "default"
		}
		key := entry.Pipeline + "/" + name
		d, ok := available[key]
		if !ok || seen[key] {
			return ArmReport{}, errors.New("local schedule selection does not match the project")
		}
		if entry.Cron != d.Trigger.Cron || entry.TZ != d.Trigger.TZ || entry.Overlap != d.Trigger.Overlap || entry.CatchUp != d.Trigger.CatchUp || !maps.Equal(entry.Args, d.Trigger.Args) {
			return ArmReport{}, errors.New("local schedule declaration differs from the project")
		}
		seen[key] = true
		selected = append(selected, d)
	}
	if input.Mode == controllerwire.ScheduleReplace && len(selected) != len(available) {
		return ArmReport{}, errors.New("replacement requires every local project schedule")
	}
	if input.Prove && prove == nil {
		return ArmReport{}, errors.New("local schedule proof is unavailable")
	}
	if !input.Prove {
		prove = nil
	}
	if s.LockPath == "" {
		return ArmReport{}, errors.New("local schedule registration requires the controller lock")
	}
	unlock, err := lockTick(s.LockPath)
	if err != nil {
		return ArmReport{}, err
	}
	defer unlock()
	seal, err := capture(ctx, input.Environment)
	if err != nil {
		return ArmReport{}, err
	}
	registration := LocalRegistration{RepoPath: root, Project: input.Project, Environment: seal}
	var batch func([]store.CronSchedule) (ArmReport, error)
	if commit != nil {
		finish, saveErr := s.preserveLocalRegistrationFiles(selected)
		if saveErr != nil {
			return ArmReport{}, saveErr
		}
		committed := false
		defer func() { err = errors.Join(err, finish(committed)) }()
		batch = func(rows []store.CronSchedule) (ArmReport, error) {
			report, commitErr := commit(ctx, rows, registration, input.Mode == controllerwire.ScheduleReplace)
			committed = commitErr == nil
			return report, commitErr
		}
	}
	return s.armDeclared(ctx, root, selected, ArmOptions{Follow: input.Follow, Prove: prove}, input.Mode == controllerwire.ScheduleReplace, func(d Declared) error {
		registration.Pipeline, registration.ScheduleName = d.Pipeline, d.Name
		return writeLocalRegistration(ctx, filepath.Dir(s.PinRoot), d.ID(), registration)
	}, batch)
}

func writeLocalRegistration(ctx context.Context, home, id string, registration LocalRegistration) (err error) {
	if !filepath.IsAbs(home) || !localScheduleID.MatchString(id) {
		return errors.New("invalid local schedule registration identity")
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	directory := filepath.Join(home, "schedule-registrations")
	if err := fssecure.EnsureDir(directory); err != nil {
		return err
	}
	if err := fssecure.SecurePrivateDir(directory); err != nil {
		return err
	}
	temporary, err := fssecure.MkdirPrivateTemp(directory, ".registration-")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(temporary)) }()
	data, err := json.Marshal(registration)
	if err != nil {
		return err
	}
	file := filepath.Join(temporary, "registration")
	if err := fssecure.WriteFile(file, data); err != nil {
		return err
	}
	return os.Rename(file, filepath.Join(directory, id+".json"))
}

// WithLocalLock serializes local edits with registration and schedule evaluation.
func (s *Service) WithLocalLock(ctx context.Context, edit func() error) error {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if s.LockPath == "" || edit == nil {
		return errors.New("local schedule edit requires the controller lock")
	}
	unlock, err := lockTick(s.LockPath)
	if err != nil {
		return err
	}
	defer unlock()
	if err := context.Cause(ctx); err != nil {
		return err
	}
	return edit()
}

func (s *Service) preserveLocalRegistrationFiles(selected []Declared) (func(bool) error, error) {
	backup, err := os.MkdirTemp(filepath.Dir(s.PinRoot), ".schedule-rollback-")
	if err != nil {
		return nil, err
	}
	type savedFile struct{ path, backup string }
	var files []savedFile
	finish := func(committed bool) error {
		var result error
		if !committed {
			for _, file := range files {
				removeErr := os.RemoveAll(file.path)
				result = errors.Join(result, removeErr)
				if removeErr == nil && file.backup != "" {
					result = errors.Join(result, os.Rename(file.backup, file.path))
				}
			}
		}
		if result == nil {
			result = os.RemoveAll(backup)
		}
		return result
	}
	for _, row := range selected {
		for _, path := range []string{filepath.Join(s.PinRoot, row.ID()), filepath.Join(filepath.Dir(s.PinRoot), "schedule-registrations", row.ID()+".json")} {
			saved := savedFile{path: path}
			if _, statErr := os.Lstat(path); statErr == nil {
				saved.backup = filepath.Join(backup, strconv.Itoa(len(files)))
				if err := os.Rename(path, saved.backup); err != nil {
					return nil, errors.Join(err, finish(false))
				}
			} else if !errors.Is(statErr, os.ErrNotExist) {
				return nil, errors.Join(statErr, finish(false))
			}
			files = append(files, saved)
		}
	}
	return finish, nil
}
