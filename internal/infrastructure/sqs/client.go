// Package sqs contains the AWS SQS adapter used by the application and its
// readiness probe. It deliberately exposes broker operations without any
// financial semantics; message translation and processing belong to the
// messaging transport.
package sqs

import (
	"context"
	"fmt"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/leonardodacosta/distributedBettingProcessing/internal/infrastructure/config"
)

// Client is the infrastructure-only SQS client. Queue URL lookup is cached
// because it is stable for a queue, while every message operation still goes
// through the AWS client and therefore receives its caller context.
type Client struct {
	api       *awssqs.Client
	queueName string

	mu       sync.RWMutex
	queueURL string
}

func NewClient(ctx context.Context, cfg config.Config) (*Client, error) {
	options := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.AWSRegion),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.AWSAccessKeyID, cfg.AWSSecretAccessKey, "")),
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, options...)
	if err != nil {
		return nil, fmt.Errorf("load AWS configuration: %w", err)
	}
	if cfg.AWSEndpointURL != "" {
		awsCfg.BaseEndpoint = aws.String(cfg.AWSEndpointURL)
	}
	return &Client{api: awssqs.NewFromConfig(awsCfg), queueName: cfg.SQSWagerQueue}, nil
}

func (c *Client) QueueURL(ctx context.Context) (string, error) {
	c.mu.RLock()
	queueURL := c.queueURL
	c.mu.RUnlock()
	if queueURL != "" {
		return queueURL, nil
	}

	response, err := c.api.GetQueueUrl(ctx, &awssqs.GetQueueUrlInput{QueueName: aws.String(c.queueName)})
	if err != nil {
		return "", err
	}
	if response.QueueUrl == nil || *response.QueueUrl == "" {
		return "", fmt.Errorf("SQS returned an empty URL for queue %q", c.queueName)
	}

	c.mu.Lock()
	if c.queueURL == "" {
		c.queueURL = *response.QueueUrl
	}
	queueURL = c.queueURL
	c.mu.Unlock()
	return queueURL, nil
}

func (c *Client) ReceiveMessage(ctx context.Context, input *awssqs.ReceiveMessageInput, options ...func(*awssqs.Options)) (*awssqs.ReceiveMessageOutput, error) {
	return c.api.ReceiveMessage(ctx, input, options...)
}

func (c *Client) DeleteMessage(ctx context.Context, input *awssqs.DeleteMessageInput, options ...func(*awssqs.Options)) (*awssqs.DeleteMessageOutput, error) {
	return c.api.DeleteMessage(ctx, input, options...)
}

func (c *Client) ChangeMessageVisibility(ctx context.Context, input *awssqs.ChangeMessageVisibilityInput, options ...func(*awssqs.Options)) (*awssqs.ChangeMessageVisibilityOutput, error) {
	return c.api.ChangeMessageVisibility(ctx, input, options...)
}

// Name and Check implement the readiness checker contract without importing
// the HTTP adapter.
func (c *Client) Name() string { return "sqs" }

func (c *Client) Check(ctx context.Context) error {
	queueURL, err := c.QueueURL(ctx)
	if err != nil {
		return err
	}
	_, err = c.api.GetQueueAttributes(ctx, &awssqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(queueURL),
		AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
	})
	return err
}
