package controller

import (
	"strings"
	"time"

	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type directUploadS3 struct {
	client  *s3.Client
	presign *s3.PresignClient
	bucket  string
	prefix  string
}

// WithDirectUploads configures S3 storage for legacy output reads and job logs.
func (s *Server) WithDirectUploads(client *s3.Client, bucket, prefix string) *Server {
	if client != nil && bucket != "" && prefix != "" {
		s.directUploads = &directUploadS3{
			client: client, presign: s3.NewPresignClient(client,
				func(o *s3.PresignOptions) {
					o.Presigner = v4.NewSigner(func(s *v4.SignerOptions) { s.DisableHeaderHoisting = true })
				}),
			bucket: bucket, prefix: strings.Trim(prefix, "/"),
		}
	}
	return s
}

const directUploadPutTTL = 15 * time.Minute
