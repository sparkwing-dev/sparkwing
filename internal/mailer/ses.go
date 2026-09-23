package mailer

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
)

// SESConfig selects the sender and routing for the SES mailer.
type SESConfig struct {
	// From is the verified sender address. Required.
	From string
	// ConfigurationSet names the SES configuration set for event
	// publishing; empty sends without one.
	ConfigurationSet string
}

// SES sends mail through the Amazon SES v2 API.
type SES struct {
	client *sesv2.Client
	cfg    SESConfig
}

// NewSES builds an SES mailer from the default AWS credential chain.
// optFns adjust the SES client, for example its endpoint.
func NewSES(ctx context.Context, cfg SESConfig, optFns ...func(*sesv2.Options)) (*SES, error) {
	if cfg.From == "" {
		return nil, errors.New("mailer: SES sender address is required")
	}
	awsCfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("mailer: load AWS config: %w", err)
	}
	return &SES{client: sesv2.NewFromConfig(awsCfg, optFns...), cfg: cfg}, nil
}

// Send delivers m through SES SendEmail.
func (s *SES) Send(ctx context.Context, m Message) error {
	in := &sesv2.SendEmailInput{
		FromEmailAddress: aws.String(s.cfg.From),
		Destination:      &types.Destination{ToAddresses: []string{m.To}},
		Content: &types.EmailContent{
			Simple: &types.Message{
				Subject: utf8Content(m.Subject),
				Body: &types.Body{
					Text: utf8Content(m.Text),
					Html: utf8Content(m.HTML),
				},
			},
		},
	}
	if s.cfg.ConfigurationSet != "" {
		in.ConfigurationSetName = aws.String(s.cfg.ConfigurationSet)
	}
	// safety: the error names the recipient and subject only; the body
	// can carry a bearer link.
	if _, err := s.client.SendEmail(ctx, in); err != nil {
		return fmt.Errorf("mailer: SES send to %s (subject %q): %w", m.To, m.Subject, err)
	}
	return nil
}

func utf8Content(s string) *types.Content {
	return &types.Content{Data: aws.String(s), Charset: aws.String("UTF-8")}
}
