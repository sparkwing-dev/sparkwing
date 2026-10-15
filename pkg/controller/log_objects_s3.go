package controller

import (
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type s3LogStorage struct {
	client *s3.Client
	bucket string
	prefix string
}

// WithS3Logs supplies the S3 bucket and namespace for retained log readers.
func (s *Server) WithS3Logs(client *s3.Client, bucket, prefix string) *Server {
	if client != nil && bucket != "" && prefix != "" {
		s.s3Logs = &s3LogStorage{
			client: client, bucket: bucket, prefix: strings.Trim(prefix, "/"),
		}
	}
	return s
}
