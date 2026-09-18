package messaging

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"

	"github.com/leonardodacosta/distributedBettingProcessing/internal/application/financial"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/domain/money"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/domain/wager"
)

type fakeQueue struct {
	queueURL          string
	deleteCalls       int
	visibilityCalls   int
	deleteAfterCommit bool
	processor         *fakeProcessor
}

func (f *fakeQueue) QueueURL(context.Context) (string, error) { return f.queueURL, nil }
func (f *fakeQueue) ReceiveMessage(context.Context, *awssqs.ReceiveMessageInput, ...func(*awssqs.Options)) (*awssqs.ReceiveMessageOutput, error) {
	return &awssqs.ReceiveMessageOutput{}, nil
}
func (f *fakeQueue) DeleteMessage(context.Context, *awssqs.DeleteMessageInput, ...func(*awssqs.Options)) (*awssqs.DeleteMessageOutput, error) {
	f.deleteCalls++
	if f.deleteAfterCommit && (f.processor == nil || !f.processor.committed) {
		return nil, errors.New("delete before commit")
	}
	return &awssqs.DeleteMessageOutput{}, nil
}
func (f *fakeQueue) ChangeMessageVisibility(context.Context, *awssqs.ChangeMessageVisibilityInput, ...func(*awssqs.Options)) (*awssqs.ChangeMessageVisibilityOutput, error) {
	f.visibilityCalls++
	return &awssqs.ChangeMessageVisibilityOutput{}, nil
}

type fakeProcessor struct {
	committed bool
	err       error
	state     wager.State
}

func (f *fakeProcessor) ProcessMessage(context.Context, string, string, string, financial.Command, time.Time) (financial.Result, bool, error) {
	if f.err != nil {
		return financial.Result{}, false, f.err
	}
	f.committed = true
	amount, _ := money.New("25.00", "BRL")
	state := f.state
	if state == "" {
		state = wager.Processed
	}
	return financial.Result{TransactionID: uuid.New(), State: state, Amount: amount}, false, nil
}

func TestConsumerDeletesOnlyAfterProcessingCommits(t *testing.T) {
	processor := &fakeProcessor{}
	queue := &fakeQueue{queueURL: "queue-url", deleteAfterCommit: true, processor: processor}
	consumer := NewConsumer(queue, processor, Config{ConsumerName: "test-consumer"})
	consumer.handleMessage(context.Background(), "queue-url", types.Message{
		Body:          aws.String(validMessage),
		ReceiptHandle: aws.String("receipt-1"),
		MessageId:     aws.String("sqs-id-1"),
		Attributes:    map[string]string{"ApproximateReceiveCount": "1"},
	})
	if queue.deleteCalls != 1 {
		t.Fatalf("delete calls = %d, want 1", queue.deleteCalls)
	}
	if queue.visibilityCalls != 0 {
		t.Fatalf("visibility calls = %d, want 0", queue.visibilityCalls)
	}
}

func TestConsumerLeavesTransientFailureForRedelivery(t *testing.T) {
	processor := &fakeProcessor{err: errors.New("temporary database outage")}
	queue := &fakeQueue{queueURL: "queue-url"}
	consumer := NewConsumer(queue, processor, Config{ConsumerName: "test-consumer", RetryVisibilityBackoff: time.Second})
	consumer.handleMessage(context.Background(), "queue-url", types.Message{
		Body:          aws.String(validMessage),
		ReceiptHandle: aws.String("receipt-1"),
		MessageId:     aws.String("sqs-id-1"),
		Attributes:    map[string]string{"ApproximateReceiveCount": "2"},
	})
	if queue.deleteCalls != 0 {
		t.Fatalf("delete calls = %d, want 0", queue.deleteCalls)
	}
	if queue.visibilityCalls != 1 {
		t.Fatalf("visibility calls = %d, want 1", queue.visibilityCalls)
	}
}

func TestConsumerDeletesDurablyRejectedBusinessResult(t *testing.T) {
	processor := &fakeProcessor{state: wager.Rejected}
	queue := &fakeQueue{queueURL: "queue-url", deleteAfterCommit: true, processor: processor}
	consumer := NewConsumer(queue, processor, Config{ConsumerName: "test-consumer"})
	consumer.handleMessage(context.Background(), "queue-url", types.Message{
		Body:          aws.String(validMessage),
		ReceiptHandle: aws.String("receipt-rejected"),
		MessageId:     aws.String("sqs-id-rejected"),
		Attributes:    map[string]string{"ApproximateReceiveCount": "1"},
	})
	if queue.deleteCalls != 1 {
		t.Fatalf("delete calls = %d, want 1", queue.deleteCalls)
	}
}

func TestConsumerRetriesMalformedMessageUntilBrokerRedrive(t *testing.T) {
	queue := &fakeQueue{queueURL: "queue-url"}
	consumer := NewConsumer(queue, &fakeProcessor{}, Config{ConsumerName: "test-consumer", RetryVisibilityBackoff: time.Second})
	consumer.handleMessage(context.Background(), "queue-url", types.Message{
		Body:          aws.String(`{"messageId":"bad"}`),
		ReceiptHandle: aws.String("receipt-1"),
		MessageId:     aws.String("sqs-id-1"),
	})
	if queue.deleteCalls != 0 || queue.visibilityCalls != 1 {
		t.Fatalf("malformed handling delete=%d visibility=%d", queue.deleteCalls, queue.visibilityCalls)
	}
}

func TestRetryDelayUsesExponentialBackoffAndVisibilityCap(t *testing.T) {
	if got := retryDelay(time.Second, 1, 30*time.Second); got != time.Second {
		t.Fatalf("first delay = %s", got)
	}
	if got := retryDelay(time.Second, 4, 5*time.Second); got != 4*time.Second {
		t.Fatalf("fourth delay = %s", got)
	}
	if got := retryDelay(time.Second, 5, 5*time.Second); got != 4*time.Second {
		t.Fatalf("capped delay = %s", got)
	}
}

type lifecycleQueue struct {
	messages       chan types.Message
	deleted        chan struct{}
	receiveStopped chan struct{}
	visibility     chan int32

	deleteOnce  sync.Once
	stopOnce    sync.Once
	visibilityM sync.Once
}

func newLifecycleQueue() *lifecycleQueue {
	return &lifecycleQueue{
		messages:       make(chan types.Message, 1),
		deleted:        make(chan struct{}),
		receiveStopped: make(chan struct{}),
		visibility:     make(chan int32, 1),
	}
}

func (q *lifecycleQueue) QueueURL(context.Context) (string, error) { return "queue-url", nil }

func (q *lifecycleQueue) ReceiveMessage(ctx context.Context, _ *awssqs.ReceiveMessageInput, _ ...func(*awssqs.Options)) (*awssqs.ReceiveMessageOutput, error) {
	select {
	case message := <-q.messages:
		return &awssqs.ReceiveMessageOutput{Messages: []types.Message{message}}, nil
	case <-ctx.Done():
		q.stopOnce.Do(func() { close(q.receiveStopped) })
		return nil, ctx.Err()
	}
}

func (q *lifecycleQueue) DeleteMessage(context.Context, *awssqs.DeleteMessageInput, ...func(*awssqs.Options)) (*awssqs.DeleteMessageOutput, error) {
	q.deleteOnce.Do(func() { close(q.deleted) })
	return &awssqs.DeleteMessageOutput{}, nil
}

func (q *lifecycleQueue) ChangeMessageVisibility(_ context.Context, input *awssqs.ChangeMessageVisibilityInput, _ ...func(*awssqs.Options)) (*awssqs.ChangeMessageVisibilityOutput, error) {
	q.visibilityM.Do(func() { q.visibility <- input.VisibilityTimeout })
	return &awssqs.ChangeMessageVisibilityOutput{}, nil
}

type lifecycleProcessor struct {
	processed chan struct{}
	once      sync.Once
}

func (p *lifecycleProcessor) ProcessMessage(context.Context, string, string, string, financial.Command, time.Time) (financial.Result, bool, error) {
	p.once.Do(func() { close(p.processed) })
	amount, _ := money.New("25.00", "BRL")
	return financial.Result{TransactionID: uuid.New(), State: wager.Processed, Amount: amount}, false, nil
}

func TestConsumerOwnsRunContextAfterStartupContextExpires(t *testing.T) {
	startupCtx, cancelStartup := context.WithCancel(context.Background())
	queue := newLifecycleQueue()
	processor := &lifecycleProcessor{processed: make(chan struct{})}
	consumer := NewConsumer(queue, processor, Config{ConsumerName: "lifecycle-test", WaitTime: time.Second})
	if err := consumer.Start(startupCtx); err != nil {
		t.Fatal(err)
	}
	cancelStartup()
	queue.messages <- types.Message{
		Body:          aws.String(validMessage),
		ReceiptHandle: aws.String("receipt-lifecycle"),
		MessageId:     aws.String("message-lifecycle"),
		Attributes:    map[string]string{"ApproximateReceiveCount": "1"},
	}

	select {
	case <-processor.processed:
	case <-time.After(time.Second):
		t.Fatal("consumer stopped when the startup context was cancelled")
	}
	select {
	case <-queue.deleted:
	case <-time.After(time.Second):
		t.Fatal("processed message was not deleted")
	}

	stopCtx, cancelStop := context.WithTimeout(context.Background(), time.Second)
	defer cancelStop()
	if err := consumer.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-queue.receiveStopped:
	case <-time.After(time.Second):
		t.Fatal("consumer polling did not stop")
	}
}

type blockingProcessor struct{ started chan struct{} }

func (p *blockingProcessor) ProcessMessage(ctx context.Context, _ string, _ string, _ string, _ financial.Command, _ time.Time) (financial.Result, bool, error) {
	close(p.started)
	<-ctx.Done()
	return financial.Result{}, false, ctx.Err()
}

func TestConsumerStopReleasesInFlightMessage(t *testing.T) {
	queue := newLifecycleQueue()
	processor := &blockingProcessor{started: make(chan struct{})}
	consumer := NewConsumer(queue, processor, Config{ConsumerName: "shutdown-test", WaitTime: time.Second})
	if err := consumer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	queue.messages <- types.Message{
		Body:          aws.String(validMessage),
		ReceiptHandle: aws.String("receipt-shutdown"),
		MessageId:     aws.String("message-shutdown"),
		Attributes:    map[string]string{"ApproximateReceiveCount": "1"},
	}
	select {
	case <-processor.started:
	case <-time.After(time.Second):
		t.Fatal("consumer did not begin processing")
	}

	stopCtx, cancelStop := context.WithTimeout(context.Background(), time.Second)
	defer cancelStop()
	if err := consumer.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	select {
	case visibility := <-queue.visibility:
		if visibility != 0 {
			t.Fatalf("release visibility = %d, want 0", visibility)
		}
	case <-time.After(time.Second):
		t.Fatal("in-flight message was not released")
	}
}

type cancelAfterReceiveQueue struct {
	message    types.Message
	cancel     func()
	visibility chan int32
	once       sync.Once
}

func (q *cancelAfterReceiveQueue) QueueURL(context.Context) (string, error) { return "queue-url", nil }

func (q *cancelAfterReceiveQueue) ReceiveMessage(context.Context, *awssqs.ReceiveMessageInput, ...func(*awssqs.Options)) (*awssqs.ReceiveMessageOutput, error) {
	q.once.Do(q.cancel)
	return &awssqs.ReceiveMessageOutput{Messages: []types.Message{q.message}}, nil
}

func (q *cancelAfterReceiveQueue) DeleteMessage(context.Context, *awssqs.DeleteMessageInput, ...func(*awssqs.Options)) (*awssqs.DeleteMessageOutput, error) {
	return &awssqs.DeleteMessageOutput{}, nil
}

func (q *cancelAfterReceiveQueue) ChangeMessageVisibility(_ context.Context, input *awssqs.ChangeMessageVisibilityInput, _ ...func(*awssqs.Options)) (*awssqs.ChangeMessageVisibilityOutput, error) {
	q.visibility <- input.VisibilityTimeout
	return &awssqs.ChangeMessageVisibilityOutput{}, nil
}

func TestConsumerReleasesReceivedButUnstartedMessagesDuringShutdown(t *testing.T) {
	processor := &lifecycleProcessor{processed: make(chan struct{})}
	var consumer *Consumer
	queue := &cancelAfterReceiveQueue{
		message: types.Message{
			Body:          aws.String(validMessage),
			ReceiptHandle: aws.String("receipt-unstarted"),
			MessageId:     aws.String("message-unstarted"),
		},
		visibility: make(chan int32, 1),
	}
	queue.cancel = func() {
		consumer.mu.Lock()
		run := consumer.active
		consumer.mu.Unlock()
		if run != nil {
			run.cancel()
		}
	}
	consumer = NewConsumer(queue, processor, Config{ConsumerName: "unstarted-shutdown-test"})
	if err := consumer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case visibility := <-queue.visibility:
		if visibility != 0 {
			t.Fatalf("unstarted message visibility = %d, want 0", visibility)
		}
	case <-time.After(time.Second):
		t.Fatal("received but unstarted message was not released")
	}
	select {
	case <-processor.processed:
		t.Fatal("consumer began financial processing after shutdown")
	default:
	}
	stopCtx, cancelStop := context.WithTimeout(context.Background(), time.Second)
	defer cancelStop()
	if err := consumer.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
}
