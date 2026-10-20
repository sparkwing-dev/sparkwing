package blob

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// LogDeleter removes physical logs in a validated tenant or run scope before record deletion.
type LogDeleter interface {
	DeleteLogs(context.Context, string, string) (int64, error)
}

func logDeletionPrefix(class, team, run string) (string, error) {
	if run != "" {
		return LogRunPrefix(class, team, run)
	}
	prefix, err := LogRunPrefix(class, team, "scope")
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(prefix, "runs/scope/"), nil
}

// DeleteLogs deletes one tenant's logs across stored classes, optionally restricted to a run, returning bytes removed.
func (f *Filesystem) DeleteLogs(ctx context.Context, team, run string) (int64, error) {
	if _, err := logDeletionPrefix("r1", team, run); err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	classes, err := fs.ReadDir(f.root.FS(), "logs")
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var deleted int64
	for _, class := range classes {
		if err := ctx.Err(); err != nil {
			return deleted, err
		}
		if !class.IsDir() {
			continue
		}
		prefix, err := logDeletionPrefix(class.Name(), team, run)
		if err != nil {
			continue
		}
		err = fs.WalkDir(f.root.FS(), "logs/"+class.Name(), func(key string, entry fs.DirEntry, walkErr error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if errors.Is(walkErr, fs.ErrNotExist) {
				return nil
			}
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				if strings.HasPrefix(prefix, key+"/") || key == strings.TrimSuffix(prefix, "/") || strings.HasPrefix(key, prefix) {
					return nil
				}
				return fs.SkipDir
			}
			if !entry.Type().IsRegular() || !strings.HasPrefix(key, prefix) {
				return nil
			}
			info, err := entry.Info()
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			if err := f.root.Remove(key); err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			deleted += info.Size()
			return nil
		})
		if err != nil {
			return deleted, err
		}
	}
	return deleted, nil
}

// DeleteLogs deletes one tenant's logs across stored classes, optionally restricted to a run, returning bytes removed.
func (s *S3) DeleteLogs(ctx context.Context, team, run string) (int64, error) {
	if _, err := logDeletionPrefix("r1", team, run); err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	logsPrefix := s.PhysicalKey("logs/")
	var deleted int64
	var token *string
	for {
		page, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(s.bucket), Prefix: aws.String(logsPrefix), Delimiter: aws.String("/"), ContinuationToken: token})
		if err != nil {
			return deleted, err
		}
		for _, class := range page.CommonPrefixes {
			name, ok := strings.CutPrefix(aws.ToString(class.Prefix), logsPrefix)
			if !ok {
				return deleted, fmt.Errorf("invalid log class listing")
			}
			prefix, err := logDeletionPrefix(strings.TrimSuffix(name, "/"), team, run)
			if err != nil {
				continue
			}
			n, err := s.deleteObjectPrefix(ctx, s.PhysicalKey(prefix))
			deleted += n
			if err != nil {
				return deleted, err
			}
		}
		if !aws.ToBool(page.IsTruncated) {
			return deleted, nil
		}
		if aws.ToString(page.NextContinuationToken) == "" || aws.ToString(page.NextContinuationToken) == aws.ToString(token) {
			return deleted, fmt.Errorf("log class listing did not advance")
		}
		token = page.NextContinuationToken
	}
}

func (s *S3) deleteObjectPrefix(ctx context.Context, prefix string) (int64, error) {
	var deleted int64
	var token *string
	for {
		page, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(s.bucket), Prefix: aws.String(prefix), ContinuationToken: token})
		if err != nil {
			return deleted, err
		}
		for _, object := range page.Contents {
			key := aws.ToString(object.Key)
			if !strings.HasPrefix(key, prefix) || aws.ToInt64(object.Size) < 0 {
				return deleted, fmt.Errorf("object listing escaped its scope")
			}
			if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)}); err != nil {
				return deleted, err
			}
			deleted += aws.ToInt64(object.Size)
		}
		if !aws.ToBool(page.IsTruncated) {
			return deleted, nil
		}
		if aws.ToString(page.NextContinuationToken) == "" || aws.ToString(page.NextContinuationToken) == aws.ToString(token) {
			return deleted, fmt.Errorf("object listing did not advance")
		}
		token = page.NextContinuationToken
	}
}

// TeamObjectDeleter removes CAS and staging bytes as well as retained logs.
type TeamObjectDeleter interface {
	DeleteTeamObjects(context.Context, string) error
}

func teamObjectPrefix(team string) (string, error) {
	if !ValidLogTeam(team) {
		return "", fmt.Errorf("invalid team object scope")
	}
	return "teams/" + team + "/", nil
}

// DeleteTeamObjects removes the team's complete physical namespace.
func (f *Filesystem) DeleteTeamObjects(ctx context.Context, team string) error {
	prefix, err := teamObjectPrefix(team)
	if err != nil {
		return err
	}
	if _, err := f.DeleteLogs(ctx, team, ""); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return f.root.RemoveAll(strings.TrimSuffix(prefix, "/"))
}

// DeleteTeamObjects removes the team's complete physical namespace.
func (s *S3) DeleteTeamObjects(ctx context.Context, team string) error {
	prefix, err := teamObjectPrefix(team)
	if err != nil {
		return err
	}
	if _, err := s.DeleteLogs(ctx, team, ""); err != nil {
		return err
	}
	_, err = s.deleteObjectPrefix(ctx, s.PhysicalKey(prefix))
	return err
}
