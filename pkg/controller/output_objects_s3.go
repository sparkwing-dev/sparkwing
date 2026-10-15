package controller

import (
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type directUploadS3 struct {
	client *s3.Client
	bucket string
	prefix string
}

// WithDirectUploads configures S3 storage for legacy output and log reads.
func (s *Server) WithDirectUploads(client *s3.Client, bucket, prefix string) *Server {
	if client != nil && bucket != "" && prefix != "" {
		s.directUploads = &directUploadS3{
			client: client,
			bucket: bucket, prefix: strings.Trim(prefix, "/"),
		}
	}
	return s
}
