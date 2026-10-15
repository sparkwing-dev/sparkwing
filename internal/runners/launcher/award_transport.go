package launcher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"

	strictjson "sigs.k8s.io/json"

	"github.com/sparkwing-dev/sparkwing/internal/credentials"
)

const awardFileName = "award.json"

// ReadBootstrapAward reads the award and token from their immutable projected Secret.
func ReadBootstrapAward(bootstrapFile string) (CloudJobAward, error) {
	if !filepath.IsAbs(bootstrapFile) || filepath.Base(bootstrapFile) == awardFileName {
		return CloudJobAward{}, errors.New("Cloud bootstrap requires an absolute token file beside award.json")
	}
	directory, err := credentials.Open(filepath.Dir(bootstrapFile))
	if err != nil {
		return CloudJobAward{}, err
	}
	metadata, err := directory.ReadBytes(awardFileName)
	if err != nil {
		return CloudJobAward{}, err
	}
	bootstrap, err := directory.ReadBytes(filepath.Base(bootstrapFile))
	if err != nil {
		return CloudJobAward{}, err
	}
	return decodeBootstrapAward(metadata, bootstrap)
}

func decodeBootstrapAward(metadata, bootstrap []byte) (CloudJobAward, error) {
	var award CloudJobAward
	strict, err := strictjson.UnmarshalStrict(metadata, &award)
	if err != nil || len(strict) != 0 {
		return CloudJobAward{}, errors.New("invalid Cloud award metadata")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(metadata, &fields); err != nil || len(fields["source"]) == 0 || bytes.Equal(bytes.TrimSpace(fields["source"]), []byte("null")) {
		return CloudJobAward{}, errors.New("Cloud award metadata requires source")
	}
	award.Bootstrap = slices.Clone(bootstrap)
	if err := validateAward(award); err != nil {
		return CloudJobAward{}, err
	}
	return award, nil
}

// RunFile restores the private award before exchanging its bootstrap once.
func (h BootstrapHost) RunFile(ctx context.Context, bootstrapFile string) error {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	award, err := ReadBootstrapAward(bootstrapFile)
	if err != nil {
		return err
	}
	return h.Run(ctx, award)
}
