package controller

import (
	"strings"

	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type s3LogStorage struct {
	client  *s3.Client
	presign *s3.PresignClient
	bucket  string
	prefix  string
}

// WithS3Logs supplies the S3 bucket and namespace for retained log readers.
func (s *Server) WithS3Logs(client *s3.Client, bucket, prefix string) *Server {
	if client != nil && bucket != "" && prefix != "" {
		s.s3Logs = &s3LogStorage{
			client: client, presign: s3.NewPresignClient(client,
				func(o *s3.PresignOptions) {
					o.Presigner = v4.NewSigner(func(s *v4.SignerOptions) { s.DisableHeaderHoisting = true })
				}),
			bucket: bucket, prefix: strings.Trim(prefix, "/"),
		}
	}
	return s
}
