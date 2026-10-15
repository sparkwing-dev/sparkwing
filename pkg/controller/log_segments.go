package controller

import (
	"github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"
	s3store "github.com/sparkwing-dev/sparkwing/pkg/storage/s3"
)

func (s *Server) logObjects() logseg.Objects {
	if s.directUploads == nil {
		return nil
	}
	d := s.directUploads
	return s3store.NewArtifactStore(d.bucket, d.prefix, d.client)
}
