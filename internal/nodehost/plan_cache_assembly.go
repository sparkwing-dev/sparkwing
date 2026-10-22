package nodehost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func (p SDKPlanner) assemblePlanCache(input *PlanningInput, request runspec.Request) {
	if input.Disabled || input.Cache != nil {
		return
	}
	directory := p.CacheDirectory
	if directory == "" {
		root, err := os.UserCacheDir()
		if err != nil {
			input.Cache = unavailablePlanCache{}
			return
		}
		directory = filepath.Join(root, "sparkwing", "plan-cache")
	}
	cache, err := NewFilePlanCache(directory, request.Team, request.Git.Repository.Key)
	if err != nil {
		input.Cache = unavailablePlanCache{}
		return
	}
	input.Cache = cache
	return
}

type unavailablePlanCache struct{}

func (unavailablePlanCache) Lookup(context.Context, PlanCacheKey) (PlanCacheLookup, error) {
	return PlanCacheLookup{}, errors.New("plan cache storage unavailable")
}

func (unavailablePlanCache) Put(context.Context, PlanCacheEntry) error {
	return errors.New("plan cache storage unavailable")
}

func (unavailablePlanCache) Invalidate(context.Context, PlanCacheKey, string) error {
	return errors.New("plan cache storage unavailable")
}

func (p SDKPlanner) fingerprintPlanning(ctx context.Context, input PlanningProcessInput, prepared PreparedPlanning, planning *PlanningInput) error {
	environment, err := p.Environment(ctx, input)
	if err != nil {
		return err
	}
	environmentValues := slices.Collect(environment.Values())
	slices.Sort(environmentValues)
	planning.Inputs.Version = 1
	planning.Inputs.EnvironmentDigest, err = runspec.CanonicalHash(environmentValues)
	if err != nil {
		return err
	}
	values := p.Masker.Values()
	slices.Sort(values)
	version, err := runspec.CanonicalHash(struct {
		Policy int
		Values []string
		Args   []string
	}{1, values, prepared.SecretArgs})
	if err != nil {
		return err
	}
	planning.Inputs.SecretVersion = string(version)
	planning.Inputs.SourceDigest, err = fingerprintTree(ctx, input.Entry.CheckoutDirectory())
	if err != nil {
		planning.Inputs.SourceDigest = ""
		(PlanningFunctions{Warn: p.Warn}).defaults().Warn("plan cache source dependencies unavailable; planning normally")
		return context.Cause(ctx)
	}
	if len(input.Entry.Command) == 0 || !filepath.IsAbs(input.Entry.Command[0]) {
		planning.Inputs.EntryDigest = ""
		(PlanningFunctions{Warn: p.Warn}).defaults().Warn("plan cache executable identity unavailable; planning normally")
		return nil
	}
	command := make([]runspec.Hash, len(input.Entry.Command))
	for i, argument := range input.Entry.Command {
		if !filepath.IsAbs(argument) {
			continue
		}
		info, statErr := os.Stat(argument)
		if statErr == nil && i > 0 && info.IsDir() {
			command[i], err = fingerprintTree(ctx, argument)
		} else {
			command[i], err = fingerprintFile(ctx, argument)
		}
		if err != nil {
			planning.Inputs.EntryDigest = ""
			(PlanningFunctions{Warn: p.Warn}).defaults().Warn("plan cache entry dependencies unavailable; planning normally")
			return context.Cause(ctx)
		}
	}
	planning.Inputs.EntryDigest, err = runspec.CanonicalHash(struct {
		Command []string
		Files   []runspec.Hash
		Tree    runspec.Hash
	}{input.Entry.Command, command, planning.Inputs.SourceDigest})
	return err
}

func fingerprintTree(ctx context.Context, directory string) (runspec.Hash, error) {
	if !filepath.IsAbs(directory) {
		return "", errors.New("source fingerprint requires an acquired root")
	}
	digest := sha256.New()
	err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := context.Cause(ctx); err != nil {
			return err
		}
		if entry.Name() == ".git" && !entry.IsDir() {
			return errors.New("source git metadata points outside the acquired tree")
		}
		metadata := filepath.ToSlash(path)
		if strings.Contains(metadata, "/.git/") && (entry.Name() == "alternates" || entry.Name() == "commondir") {
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if len(strings.TrimSpace(string(body))) != 0 {
				return errors.New("source git metadata has external dependencies")
			}
		}
		relative, err := filepath.Rel(directory, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("source dependencies contain a nonregular file")
		}
		file, err := fingerprintFile(ctx, path)
		if err != nil {
			return err
		}
		return json.NewEncoder(digest).Encode(struct {
			Path   string
			Mode   fs.FileMode
			Digest runspec.Hash
		}{filepath.ToSlash(relative), info.Mode().Perm(), file})
	})
	if err != nil {
		return "", err
	}
	return runspec.Hash(hex.EncodeToString(digest.Sum(nil))), nil
}

func fingerprintFile(ctx context.Context, path string) (runspec.Hash, error) {
	if err := context.Cause(ctx); err != nil {
		return "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("entry is not a regular file")
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	if err := context.Cause(ctx); err != nil {
		return "", err
	}
	return runspec.Hash(hex.EncodeToString(digest.Sum(nil))), nil
}
