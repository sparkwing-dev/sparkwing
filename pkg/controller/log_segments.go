package controller

import (
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
	s3store "github.com/sparkwing-dev/sparkwing/pkg/storage/s3"
)

func (s *Server) logObjects() logseg.SegmentObjects {
	if s.s3Logs == nil {
		return nil
	}
	d := s.s3Logs
	return s3store.NewArtifactStore(d.bucket, d.prefix, d.client)
}
