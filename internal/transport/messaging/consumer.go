package messaging

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/leonardodacosta/distributedBettingProcessing/internal/application/financial"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/observability"
)

const defaultConsumerName = "wager-transaction-consumer"

type Queue interface {
	QueueURL(context.Context) (string, error)
	ReceiveMessage(context.Context, *awssqs.ReceiveMessageInput, ...func(*awssqs.Options)) (*awssqs.ReceiveMessageOutput, error)
	DeleteMessage(context.Context, *awssqs.DeleteMessageInput, ...func(*awssqs.Options)) (*awssqs.DeleteMessageOutput, error)
	ChangeMessageVisibility(context.Context, *awssqs.ChangeMessageVisibilityInput, ...func(*awssqs.Options)) (*awssqs.ChangeMessageVisibilityOutput, error)
}

type Processor interface {
	ProcessMessage(context.Context, string, string, string, financial.Command, time.Time) (financial.Result, bool, error)
}

type Config struct {
	ConsumerName           string
	VisibilityTimeout      time.Duration
	WaitTime               time.Duration
	MaxMessages            int32
	RetryVisibilityBackoff time.Duration
	Logger                 *slog.Logger
	Metrics                *observability.Metrics
	MaxReceiveCount        int
}

type Consumer struct {
	queue     Queue
	processor Processor
	config    Config

	mu     sync.Mutex
	active *consumerRun
}

// consumerRun owns the lifetime context of one consumer invocation. Fx gives
// OnStart a context that expires as soon as startup has completed, so that
// context can only validate startup; it cannot own a background poll loop.
// Stop owns cancellation of this context and waits for done before the
// dependency lifecycle is allowed to continue.
type consumerRun struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func NewConsumer(queue Queue, processor Processor, config Config) *Consumer {
	if config.ConsumerName == "" {
		config.ConsumerName = defaultConsumerName
	}
	if config.MaxMessages <= 0 || config.MaxMessages > 10 {
		config.MaxMessages = 1
	}
	if config.WaitTime < 0 || config.WaitTime > 20*time.Second {
		config.WaitTime = 10 * time.Second
	}
	if config.VisibilityTimeout <= 0 {
		config.VisibilityTimeout = 30 * time.Second
	}
	if config.RetryVisibilityBackoff < 0 {
		config.RetryVisibilityBackoff = 0
	}
	return &Consumer{queue: queue, processor: processor, config: config}
}

func (c *Consumer) Start(startupCtx context.Context) error {
	if err := startupCtx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active != nil {
		return errors.New("SQS consumer already started")
	}
	// The consumer explicitly owns this context. It intentionally does not
	// derive from startupCtx because Fx cancels OnStart's context after a
	// successful startup. Stop is responsible for cancellation and for waiting
	// until the polling goroutine has exited.
	runContext, cancel := context.WithCancel(context.Background())
	run := &consumerRun{cancel: cancel, done: make(chan struct{})}
	c.active = run
	go func() {
		defer close(run.done)
		c.run(runContext)
	}()
	return nil
}

func (c *Consumer) Stop(ctx context.Context) error {
	c.mu.Lock()
	run := c.active
	c.mu.Unlock()
	if run == nil {
		return nil
	}
	run.cancel()
	select {
	case <-run.done:
		c.mu.Lock()
		if c.active == run {
			c.active = nil
		}
		c.mu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Consumer) run(ctx context.Context) {
	var queueURL string
	for {
		if ctx.Err() != nil {
			return
		}
		if queueURL == "" {
			resolved, err := c.queue.QueueURL(ctx)
			if err != nil {
				c.logError("SQS queue lookup failed", err)
				if !wait(ctx, time.Second) {
					return
				}
				continue
			}
			queueURL = resolved
		}

		messages, err := c.queue.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{
			QueueUrl:                    aws.String(queueURL),
			MaxNumberOfMessages:         c.config.MaxMessages,
			WaitTimeSeconds:             int32(c.config.WaitTime / time.Second),
			VisibilityTimeout:           int32(c.config.VisibilityTimeout / time.Second),
			MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameApproximateReceiveCount},
		})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			c.logError("SQS receive failed", err)
			if !wait(ctx, time.Second) {
				return
			}
			continue
		}
		for index, message := range messages.Messages {
			if ctx.Err() != nil {
				// Receive may return a batch concurrently with shutdown. Those
				// receipt handles were acquired but no financial work has begun,
				// so release every unstarted message immediately instead of making
				// redelivery wait for the full visibility timeout.
				for _, unstarted := range messages.Messages[index:] {
					handle := stringValue(unstarted.ReceiptHandle)
					if handle != "" {
						c.release(queueURL, handle)
					}
				}
				return
			}
			c.handleMessage(ctx, queueURL, message)
		}
	}
}

func (c *Consumer) handleMessage(ctx context.Context, queueURL string, message types.Message) {
	handle := stringValue(message.ReceiptHandle)
	attempt := receiveAttempt(message)
	if handle == "" {
		c.logError("SQS message has no receipt handle", ErrMalformedMessage)
		return
	}
	body := stringValue(message.Body)
	decoded, err := DecodeMessage(body)
	if err != nil {
		c.logMessageError("SQS message rejected by envelope validation", message, err)
		if c.config.MaxReceiveCount > 0 && attempt >= c.config.MaxReceiveCount && c.config.Metrics != nil {
			c.config.Metrics.ObserveRedriveCandidate()
		}
		c.retry(ctx, queueURL, handle, attempt)
		return
	}

	messageCtx := observability.WithCorrelation(ctx, decoded.MessageID)
	result, duplicate, err := c.processor.ProcessMessage(messageCtx, c.config.ConsumerName, decoded.MessageID, decoded.PayloadHash, decoded.Command, time.Now().UTC())
	if err != nil {
		if ctx.Err() != nil {
			c.release(queueURL, handle)
			return
		}
		c.logMessageError("SQS financial processing failed; message remains retryable", message, err)
		if c.config.MaxReceiveCount > 0 && attempt >= c.config.MaxReceiveCount && c.config.Metrics != nil {
			c.config.Metrics.ObserveRedriveCandidate()
		}
		c.retry(ctx, queueURL, handle, attempt)
		return
	}

	_, err = c.queue.DeleteMessage(ctx, &awssqs.DeleteMessageInput{QueueUrl: aws.String(queueURL), ReceiptHandle: aws.String(handle)})
	if err != nil {
		// The financial commit already happened. Leaving the message visible is
		// safe because the inbox/idempotency records make redelivery a replay.
		if ctx.Err() != nil {
			c.release(queueURL, handle)
		}
		c.logMessageError("SQS delete failed after durable commit", message, err)
		return
	}
	if c.config.Logger != nil {
		c.config.Logger.Info("SQS message committed and deleted",
			slog.String("messageId", decoded.MessageID),
			slog.String("correlationId", observability.Correlation(messageCtx)),
			slog.String("transactionId", result.TransactionID.String()),
			slog.String("walletId", decoded.Command.WalletID.String()),
			slog.String("providerId", decoded.Command.ProviderID),
			slog.String("status", string(result.State)),
			slog.Bool("idempotentReplay", duplicate),
		)
	}
}

func (c *Consumer) retry(ctx context.Context, queueURL, receiptHandle string, attempt int) {
	if c.config.Metrics != nil {
		c.config.Metrics.ObserveRetry("sqs")
	}
	delay := retryDelay(c.config.RetryVisibilityBackoff, attempt, c.config.VisibilityTimeout)
	visibility := int32(delay / time.Second)
	if delay > 0 && visibility == 0 {
		visibility = 1
	}
	_, err := c.queue.ChangeMessageVisibility(ctx, &awssqs.ChangeMessageVisibilityInput{QueueUrl: aws.String(queueURL), ReceiptHandle: aws.String(receiptHandle), VisibilityTimeout: visibility})
	if err != nil && ctx.Err() == nil {
		c.logError("SQS visibility update failed", err)
	}
}

func (c *Consumer) release(queueURL, receiptHandle string) {
	releaseContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = c.queue.ChangeMessageVisibility(releaseContext, &awssqs.ChangeMessageVisibilityInput{QueueUrl: aws.String(queueURL), ReceiptHandle: aws.String(receiptHandle), VisibilityTimeout: 0})
}

func (c *Consumer) logError(message string, err error) {
	if c.config.Logger != nil {
		c.config.Logger.Error(message, slog.String("error", err.Error()))
	}
}

func (c *Consumer) logMessageError(message string, received types.Message, err error) {
	if c.config.Logger != nil {
		messageID := stringValue(received.MessageId)
		c.config.Logger.Error(message,
			slog.String("messageId", messageID),
			slog.String("correlationId", messageID),
			slog.String("error", err.Error()))
	}
}

func receiveAttempt(message types.Message) int {
	raw := message.Attributes["ApproximateReceiveCount"]
	attempt, err := strconv.Atoi(raw)
	if err != nil || attempt < 1 {
		return 1
	}
	return attempt
}

func retryDelay(base time.Duration, attempt int, visibility time.Duration) time.Duration {
	if base <= 0 || visibility <= 0 {
		return 0
	}
	if attempt < 1 {
		attempt = 1
	}
	shift := attempt - 1
	if shift > 30 {
		shift = 30
	}
	delay := base * time.Duration(uint64(1)<<shift)
	if delay < 0 || delay > visibility-time.Second {
		return maxDuration(0, visibility-time.Second)
	}
	return delay
}

func maxDuration(left, right time.Duration) time.Duration {
	if left > right {
		return left
	}
	return right
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func wait(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
