package logseg

import "github.com/sparkwing-dev/sparkwing/internal/blob"

func cloneSubject(s blob.LogSubject) blob.LogSubject {
	if s.Attempt != nil {
		a := *s.Attempt
		s.Attempt = &a
	}
	if s.Unit != nil {
		u := *s.Unit
		s.Unit = &u
		if u.Source != nil {
			source := *u.Source
			s.Unit.Source = &source
		}
	}
	return s
}
