package controller

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// WithLogRetention keeps the log segments of the named teams for the
// given number of days instead of the built-in 90 (with credits) or 30
// (without). Each distinct number is a retention class under
// logs/r<days>/, which needs its own lifecycle rule in the bucket.
func (s *Server) WithLogRetention(days map[store.Team]int) *Server {
	s.logRetention = maps.Clone(days)
	return s
}

func (s *Server) logClasses() []string {
	classes := []string{logseg.ClassStandard, logseg.ClassFree}
	for _, days := range s.logRetention {
		if class := "r" + strconv.Itoa(days); !slices.Contains(classes, class) {
			classes = append(classes, class)
		}
	}
	slices.Sort(classes)
	return classes
}

// safety: the class is fixed when an attempt's first URLs are issued, so a team that gains or loses credits
// changes the class of new attempts only and never moves a log a lifecycle rule already counts.
func (s *Server) logClassFor(team store.Team, tier store.TeamStorageTier) string {
	if days, ok := s.logRetention[team]; ok {
		return "r" + strconv.Itoa(days)
	}
	if tier == store.TeamTierFree {
		return logseg.ClassFree
	}
	return logseg.ClassStandard
}

// safety: the controller never writes lifecycle rules, because a put
// replaces the bucket's whole configuration, including rules the operator
// owns for other prefixes; it only reads them and refuses to write a log
// that nothing would expire.
type logLifecycle struct {
	mu      sync.Mutex
	checked time.Time
	missing map[string]error
}

// perf: an operator who adds a missing rule is noticed within this long,
// without a restart, and a bucket is not asked on every request.
const logLifecycleRecheck = 5 * time.Minute

// safety: a controller that keeps logs on its own disk has no lifecycle to check; its logs stay until their run
// is deleted, which the operator chose by configuring no bucket.
func (s *Server) logClassReady(ctx context.Context, class string) error {
	d := s.directUploads
	if d == nil {
		return nil
	}
	l := &s.logLifecycle
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.missing == nil || time.Since(l.checked) >= logLifecycleRecheck {
		l.missing, l.checked = s.checkLogLifecycle(ctx, d), time.Now()
		for c, err := range l.missing {
			s.logger.Error("log segments: retention class has no lifecycle rule; logs of this class stay on the runner",
				"class", c, "err", err)
		}
	}
	return l.missing[class]
}

func (s *Server) checkLogLifecycle(ctx context.Context, d *directUploadS3) map[string]error {
	missing := map[string]error{}
	out, err := d.client.GetBucketLifecycleConfiguration(ctx, &s3.GetBucketLifecycleConfigurationInput{Bucket: aws.String(d.bucket)})
	var rules []types.LifecycleRule
	var api smithy.APIError
	switch {
	case err == nil:
		rules = out.Rules
	case errors.As(err, &api) && api.ErrorCode() == "NoSuchLifecycleConfiguration":
	default:
		for _, class := range s.logClasses() {
			missing[class] = fmt.Errorf("read the bucket's lifecycle rules: %w", err)
		}
		return missing
	}
	for _, class := range s.logClasses() {
		days, _ := blob.LogClassDays(class)
		prefix := d.prefix + "/" + blob.LogClassPrefix(class)
		if !blob.LogLifecycleExpires(rules, prefix, days) {
			missing[class] = fmt.Errorf("bucket %s has no enabled lifecycle rule expiring prefix %q after %d days; add one",
				d.bucket, prefix, days)
		}
	}
	return missing
}
