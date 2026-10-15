package controller

import "github.com/sparkwing-dev/sparkwing/pkg/storage/logseg"

func (s *Server) logObjects() logseg.SegmentObjects {
	if s.dataController == nil {
		return nil
	}
	objects, _ := s.dataController.Objects.(logseg.SegmentObjects)
	return objects
}
