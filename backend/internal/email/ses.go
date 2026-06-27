// Package email sends transactional email via AWS SES.
package email

import (
	"context"
	"fmt"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"

	appconfig "github.com/alifyandra/portfolio-site/backend/internal/config"
)

// ErrNotConfigured is returned when no SES sender is configured.
var ErrNotConfigured = fmt.Errorf("email: SES sender not configured")

// Mailer sends email through SES. When no sender is configured it is still
// constructed, but Send returns ErrNotConfigured so callers can degrade.
type Mailer struct {
	client   *sesv2.Client
	sender   string
	notifyTo string
}

// New builds a Mailer from config.
func New(ctx context.Context, cfg *appconfig.Config) (*Mailer, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.AWSRegion))
	if err != nil {
		return nil, fmt.Errorf("loading aws config: %w", err)
	}
	return &Mailer{
		client:   sesv2.NewFromConfig(awsCfg),
		sender:   cfg.SESSenderEmail,
		notifyTo: cfg.ContactNotifyTo,
	}, nil
}

// Configured reports whether a sender is set.
func (m *Mailer) Configured() bool { return m.sender != "" }

// NotifyTo is the configured contact-notification recipient.
func (m *Mailer) NotifyTo() string { return m.notifyTo }

// Send delivers a plain-text email. replyTo may be empty.
func (m *Mailer) Send(ctx context.Context, to, subject, body, replyTo string) error {
	if !m.Configured() {
		return ErrNotConfigured
	}
	in := &sesv2.SendEmailInput{
		FromEmailAddress: &m.sender,
		Destination:      &types.Destination{ToAddresses: []string{to}},
		Content: &types.EmailContent{
			Simple: &types.Message{
				Subject: &types.Content{Data: &subject},
				Body:    &types.Body{Text: &types.Content{Data: &body}},
			},
		},
	}
	if replyTo != "" {
		in.ReplyToAddresses = []string{replyTo}
	}
	if _, err := m.client.SendEmail(ctx, in); err != nil {
		return fmt.Errorf("sending email: %w", err)
	}
	return nil
}
