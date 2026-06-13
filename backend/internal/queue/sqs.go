package queue

import (
	"context"
	"encoding/json"
	"fmt"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	appconfig "github.com/alifyandra/portfolio-site/backend/internal/config"
)

// Job is the envelope placed on the queue. Type routes it to a handler in the
// worker; Payload is the type-specific JSON body. No real jobs exist yet — this
// is the seam for the future LLM/async work (see ADR 0007).
type Job struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// Client is a thin SQS producer/consumer.
type Client struct {
	sqs      *sqs.Client
	queueURL string
}

// New builds an SQS client. cfg.SQSEndpoint points at ElasticMQ locally; empty
// in prod for real SQS.
func New(ctx context.Context, cfg *appconfig.Config) (*Client, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.AWSRegion))
	if err != nil {
		return nil, fmt.Errorf("loading aws config: %w", err)
	}
	client := sqs.NewFromConfig(awsCfg, func(o *sqs.Options) {
		if cfg.SQSEndpoint != "" {
			o.BaseEndpoint = &cfg.SQSEndpoint
		}
	})
	return &Client{sqs: client, queueURL: cfg.SQSQueueURL}, nil
}

// Enqueue places a job on the queue.
func (c *Client) Enqueue(ctx context.Context, job Job) error {
	body, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("marshalling job: %w", err)
	}
	s := string(body)
	_, err = c.sqs.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:    &c.queueURL,
		MessageBody: &s,
	})
	if err != nil {
		return fmt.Errorf("sending message: %w", err)
	}
	return nil
}

// Receive long-polls for up to maxMessages jobs.
func (c *Client) Receive(ctx context.Context, maxMessages int32) ([]Received, error) {
	out, err := c.sqs.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            &c.queueURL,
		MaxNumberOfMessages: maxMessages,
		WaitTimeSeconds:     20, // long poll
	})
	if err != nil {
		return nil, fmt.Errorf("receiving messages: %w", err)
	}
	received := make([]Received, 0, len(out.Messages))
	for _, m := range out.Messages {
		var job Job
		if err := json.Unmarshal([]byte(*m.Body), &job); err != nil {
			// Malformed message: skip (a real impl might dead-letter it).
			continue
		}
		received = append(received, Received{Job: job, receiptHandle: m.ReceiptHandle})
	}
	return received, nil
}

// Delete acknowledges a successfully-processed message.
func (c *Client) Delete(ctx context.Context, r Received) error {
	_, err := c.sqs.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      &c.queueURL,
		ReceiptHandle: r.receiptHandle,
	})
	if err != nil {
		return fmt.Errorf("deleting message: %w", err)
	}
	return nil
}

// Received is a job pulled off the queue, carrying the handle needed to ack it.
type Received struct {
	Job           Job
	receiptHandle *string
}
