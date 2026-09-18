package messaging

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/leonardodacosta/distributedBettingProcessing/internal/application/financial"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/domain/money"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/domain/wager"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/infrastructure/config"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/infrastructure/postgres"
	sqsinfrastructure "github.com/leonardodacosta/distributedBettingProcessing/internal/infrastructure/sqs"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/observability"
)

type localStackFixture struct {
	databaseURL string
	config      config.Config
	api         *awssqs.Client
}

func requireLocalStack(t *testing.T, requireDatabase bool) localStackFixture {
	t.Helper()
	endpoint := os.Getenv("AWS_ENDPOINT_URL")
	if endpoint == "" {
		t.Skip("AWS_ENDPOINT_URL is required")
	}
	databaseURL := os.Getenv("DATABASE_URL")
	if requireDatabase && databaseURL == "" {
		t.Skip("DATABASE_URL is required")
	}
	region := valueOrTest(os.Getenv("AWS_REGION"), "us-east-1")
	accessKeyID := valueOrTest(os.Getenv("AWS_ACCESS_KEY_ID"), "test")
	secretAccessKey := valueOrTest(os.Getenv("AWS_SECRET_ACCESS_KEY"), "test")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, "")),
	)
	if err != nil {
		t.Fatal(err)
	}
	awsCfg.BaseEndpoint = aws.String(endpoint)
	return localStackFixture{
		databaseURL: databaseURL,
		config: config.Config{
			AWSRegion:          region,
			AWSEndpointURL:     endpoint,
			AWSAccessKeyID:     accessKeyID,
			AWSSecretAccessKey: secretAccessKey,
		},
		api: awssqs.NewFromConfig(awsCfg),
	}
}

type sqsTopology struct {
	mainName, mainURL string
	dlqName, dlqURL   string
	dlqARN            string
}

func createSQSIntegrationTopology(ctx context.Context, t *testing.T, api *awssqs.Client, visibilitySeconds, maxReceives int32) sqsTopology {
	t.Helper()
	prefix := "loop7-" + uuid.New().String()
	dlqName := prefix + "-dlq.fifo"
	dlq, err := api.CreateQueue(ctx, &awssqs.CreateQueueInput{
		QueueName: aws.String(dlqName),
		Attributes: map[string]string{
			"FifoQueue":                     "true",
			"ContentBasedDeduplication":     "true",
			"VisibilityTimeout":             strconv.Itoa(int(visibilitySeconds)),
			"ReceiveMessageWaitTimeSeconds": "1",
		},
	})
	if err != nil || dlq.QueueUrl == nil {
		t.Fatalf("create DLQ: url=%v error=%v", dlq.QueueUrl, err)
	}
	dlqAttributes, err := api.GetQueueAttributes(ctx, &awssqs.GetQueueAttributesInput{
		QueueUrl:       dlq.QueueUrl,
		AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn, types.QueueAttributeNameFifoQueue},
	})
	if err != nil || dlqAttributes.Attributes["QueueArn"] == "" || dlqAttributes.Attributes["FifoQueue"] != "true" {
		t.Fatalf("DLQ attributes: %+v error=%v", dlqAttributes.Attributes, err)
	}

	mainName := prefix + ".fifo"
	redrive := fmt.Sprintf(`{"deadLetterTargetArn":%q,"maxReceiveCount":%q}`, dlqAttributes.Attributes["QueueArn"], strconv.Itoa(int(maxReceives)))
	main, err := api.CreateQueue(ctx, &awssqs.CreateQueueInput{
		QueueName: aws.String(mainName),
		Attributes: map[string]string{
			"FifoQueue":                     "true",
			"ContentBasedDeduplication":     "true",
			"VisibilityTimeout":             strconv.Itoa(int(visibilitySeconds)),
			"ReceiveMessageWaitTimeSeconds": "1",
			"RedrivePolicy":                 redrive,
		},
	})
	if err != nil || main.QueueUrl == nil {
		t.Fatalf("create main queue: url=%v error=%v", main.QueueUrl, err)
	}
	mainAttributes, err := api.GetQueueAttributes(ctx, &awssqs.GetQueueAttributesInput{
		QueueUrl: main.QueueUrl,
		AttributeNames: []types.QueueAttributeName{
			types.QueueAttributeNameFifoQueue,
			types.QueueAttributeNameVisibilityTimeout,
			types.QueueAttributeNameRedrivePolicy,
		},
	})
	if err != nil || mainAttributes.Attributes["FifoQueue"] != "true" || mainAttributes.Attributes["VisibilityTimeout"] != strconv.Itoa(int(visibilitySeconds)) || mainAttributes.Attributes["RedrivePolicy"] != redrive {
		t.Fatalf("main queue attributes: %+v error=%v", mainAttributes.Attributes, err)
	}
	topology := sqsTopology{mainName: mainName, mainURL: *main.QueueUrl, dlqName: dlqName, dlqURL: *dlq.QueueUrl, dlqARN: dlqAttributes.Attributes["QueueArn"]}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := api.DeleteQueue(cleanupCtx, &awssqs.DeleteQueueInput{QueueUrl: aws.String(topology.mainURL)}); err != nil {
			t.Logf("delete temporary main queue %q: %v", topology.mainName, err)
		}
		if _, err := api.DeleteQueue(cleanupCtx, &awssqs.DeleteQueueInput{QueueUrl: aws.String(topology.dlqURL)}); err != nil {
			t.Logf("delete temporary DLQ %q: %v", topology.dlqName, err)
		}
	})
	return topology
}

func valueOrTest(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func messageBody(messageID, providerID, externalID, idempotencyKey, playerID string, walletID uuid.UUID, amount string) string {
	return `{"messageId":"` + messageID + `","type":"WagerTransactionRequested","occurredAt":"2026-09-08T12:00:00.000Z","data":{"providerId":"` + providerID + `","externalTransactionId":"` + externalID + `","idempotencyKey":"` + idempotencyKey + `","playerId":"` + playerID + `","walletId":"` + walletID.String() + `","roundId":"round-sqs","gameId":"game-sqs","kind":"BET","money":{"amount":"` + amount + `","currency":"BRL"}}}`
}

func sendFIFO(ctx context.Context, api *awssqs.Client, queueURL, body, groupID, deduplicationID string) error {
	_, err := api.SendMessage(ctx, &awssqs.SendMessageInput{
		QueueUrl:               aws.String(queueURL),
		MessageBody:            aws.String(body),
		MessageGroupId:         aws.String(groupID),
		MessageDeduplicationId: aws.String(deduplicationID),
	})
	return err
}

func eventually(ctx context.Context, fn func() (bool, error)) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		ok, err := fn()
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

type completionCheckingQueue struct {
	Queue
	inbox        *postgres.InboxRepository
	consumerName string
	messageID    string

	mu          sync.Mutex
	deleteCalls int
}

func (q *completionCheckingQueue) DeleteMessage(ctx context.Context, input *awssqs.DeleteMessageInput, options ...func(*awssqs.Options)) (*awssqs.DeleteMessageOutput, error) {
	record, err := q.inbox.Find(ctx, q.consumerName, q.messageID)
	if err != nil {
		return nil, fmt.Errorf("read inbox before SQS delete: %w", err)
	}
	if record.CompletedAt == nil {
		return nil, errors.New("SQS delete attempted before inbox completion")
	}
	output, err := q.Queue.DeleteMessage(ctx, input, options...)
	if err != nil {
		return output, err
	}
	q.mu.Lock()
	q.deleteCalls++
	q.mu.Unlock()
	return output, nil
}

func (q *completionCheckingQueue) DeleteCalls() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.deleteCalls
}

type failFirstDeleteQueue struct {
	Queue

	mu          sync.Mutex
	deleteCalls int
	succeeded   int
}

func (q *failFirstDeleteQueue) DeleteMessage(ctx context.Context, input *awssqs.DeleteMessageInput, options ...func(*awssqs.Options)) (*awssqs.DeleteMessageOutput, error) {
	q.mu.Lock()
	q.deleteCalls++
	call := q.deleteCalls
	q.mu.Unlock()
	if call == 1 {
		return nil, errors.New("injected SQS delete failure")
	}
	output, err := q.Queue.DeleteMessage(ctx, input, options...)
	if err != nil {
		return output, err
	}
	q.mu.Lock()
	q.succeeded++
	q.mu.Unlock()
	return output, nil
}

func (q *failFirstDeleteQueue) DeleteCalls() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.deleteCalls
}

func (q *failFirstDeleteQueue) SuccessfulDeletes() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.succeeded
}

type holdingProcessor struct {
	Processor
	entered chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (p *holdingProcessor) ProcessMessage(ctx context.Context, consumerName, messageID, payloadHash string, command financial.Command, now time.Time) (financial.Result, bool, error) {
	result, duplicate, err := p.Processor.ProcessMessage(ctx, consumerName, messageID, payloadHash, command, now)
	p.once.Do(func() { close(p.entered) })
	select {
	case <-p.release:
		return result, duplicate, err
	case <-ctx.Done():
		return result, duplicate, ctx.Err()
	}
}

type recordedDeleteQueue struct {
	Queue
	deleted chan struct{}
	once    sync.Once
}

// crashBeforeDeleteQueue belongs only to the failure-engineering test harness.
// It writes a signal after ProcessMessage has returned and then deliberately
// never returns from DeleteMessage. The parent test kills this test process at
// that precise acknowledgement window; production code has no crash hook.
type crashBeforeDeleteQueue struct {
	Queue
	signalPath string
	once       sync.Once
}

func (q *crashBeforeDeleteQueue) DeleteMessage(ctx context.Context, input *awssqs.DeleteMessageInput, options ...func(*awssqs.Options)) (*awssqs.DeleteMessageOutput, error) {
	q.once.Do(func() {
		if err := os.WriteFile(q.signalPath, []byte("after-commit-before-delete"), 0o600); err != nil {
			panic(err)
		}
	})
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestLoop11ConsumerCrashAfterCommitBeforeDeleteHelper(t *testing.T) {
	if os.Getenv("LOOP11_CONSUMER_CRASH_HELPER") != "1" {
		return
	}
	fixture := requireLocalStack(t, true)
	fixture.config.SQSWagerQueue = os.Getenv("LOOP11_WAGER_QUEUE")
	queue, err := sqsinfrastructure.NewClient(context.Background(), fixture.config)
	if err != nil {
		t.Fatal(err)
	}
	db, err := postgres.NewRepository(context.Background(), fixture.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	consumer := NewConsumer(&crashBeforeDeleteQueue{Queue: queue, signalPath: os.Getenv("LOOP11_SIGNAL_FILE")}, financial.NewService(db), Config{
		ConsumerName:           os.Getenv("LOOP11_CONSUMER_NAME"),
		VisibilityTimeout:      time.Second,
		WaitTime:               time.Second,
		RetryVisibilityBackoff: 0,
	})
	if err := consumer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {}
}

func TestLoop11ConsumerProcessCrashAfterCommitBeforeDeleteRedeliversSafely(t *testing.T) {
	fixture := requireLocalStack(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	topology := createSQSIntegrationTopology(ctx, t, fixture.api, 1, 5)
	fixture.config.SQSWagerQueue = topology.mainName
	fixture.config.SQSWagerDLQ = topology.dlqName
	db, err := postgres.NewRepository(ctx, fixture.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := financial.NewService(db)
	walletID := uuid.New()
	playerID := "loop11-crash-player-" + walletID.String()
	if err := service.OpenWallet(ctx, walletID, playerID, moneyMustTest("100.00", "BRL"), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	messageID := "loop11-crash-" + uuid.NewString()
	providerID := "loop11-provider-" + uuid.NewString()
	externalID := "loop11-external-" + uuid.NewString()
	consumerName := "loop11-crash-consumer"
	body := messageBody(messageID, providerID, externalID, "idem-"+messageID, playerID, walletID, "25.00")
	signalPath := filepath.Join(t.TempDir(), "after-commit-before-delete")
	child := exec.Command(os.Args[0], "-test.run=^TestLoop11ConsumerCrashAfterCommitBeforeDeleteHelper$")
	child.Env = append(os.Environ(),
		"LOOP11_CONSUMER_CRASH_HELPER=1",
		"LOOP11_SIGNAL_FILE="+signalPath,
		"LOOP11_WAGER_QUEUE="+topology.mainName,
		"LOOP11_CONSUMER_NAME="+consumerName,
	)
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if child.Process != nil {
			_ = child.Process.Kill()
			_, _ = child.Process.Wait()
		}
	})
	if err := sendFIFO(ctx, fixture.api, topology.mainURL, body, walletID.String(), messageID); err != nil {
		t.Fatal(err)
	}
	if err := eventually(ctx, func() (bool, error) {
		_, err := os.Stat(signalPath)
		return err == nil, nil
	}); err != nil {
		t.Fatalf("consumer did not enter post-commit/delete window: %v", err)
	}
	transaction, err := postgres.NewWagerTransactionRepository(db).FindByExternal(ctx, providerID, externalID)
	if err != nil || transaction.State != string(wager.Processed) {
		t.Fatalf("durable transaction before crash = %+v, error=%v", transaction, err)
	}
	inbox, err := postgres.NewInboxRepository(db).Find(ctx, consumerName, messageID)
	if err != nil || inbox.CompletedAt == nil {
		t.Fatalf("durable inbox before crash = %+v, error=%v", inbox, err)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err == nil {
		t.Fatal("crash helper unexpectedly exited cleanly")
	}
	child.Process = nil

	queue, err := sqsinfrastructure.NewClient(ctx, fixture.config)
	if err != nil {
		t.Fatal(err)
	}
	deleted := make(chan struct{})
	restarted := NewConsumer(&recordedDeleteQueue{Queue: queue, deleted: deleted}, financial.NewService(db), Config{
		ConsumerName:           consumerName,
		VisibilityTimeout:      time.Second,
		WaitTime:               time.Second,
		RetryVisibilityBackoff: 0,
	})
	if err := restarted.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = restarted.Stop(stopCtx)
	})
	if err := eventually(ctx, func() (bool, error) {
		select {
		case <-deleted:
			return true, nil
		default:
			return false, nil
		}
	}); err != nil {
		t.Fatalf("redelivery after consumer crash was not deleted: %v", err)
	}
	if count, countErr := postgres.NewWagerTransactionRepository(db).CountByExternal(ctx, providerID, externalID); countErr != nil || count != 1 {
		t.Fatalf("transactions after crash redelivery = %d, error=%v", count, countErr)
	}
	if count, countErr := postgres.NewLedgerRepository(db).CountByTransaction(ctx, transaction.ID); countErr != nil || count != 1 {
		t.Fatalf("ledger entries after crash redelivery = %d, error=%v", count, countErr)
	}
}

func (q *recordedDeleteQueue) DeleteMessage(ctx context.Context, input *awssqs.DeleteMessageInput, options ...func(*awssqs.Options)) (*awssqs.DeleteMessageOutput, error) {
	output, err := q.Queue.DeleteMessage(ctx, input, options...)
	if err == nil {
		q.once.Do(func() { close(q.deleted) })
	}
	return output, err
}

func TestSQSConsumerAgainstLocalStackAndPostgres(t *testing.T) {
	fixture := requireLocalStack(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	topology := createSQSIntegrationTopology(ctx, t, fixture.api, 2, 3)
	fixture.config.SQSWagerQueue = topology.mainName
	fixture.config.SQSWagerDLQ = topology.dlqName
	queue, err := sqsinfrastructure.NewClient(ctx, fixture.config)
	if err != nil {
		t.Fatal(err)
	}
	db, err := postgres.NewRepository(ctx, fixture.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	metrics := observability.NewMetrics()
	service := financial.NewService(db, metrics)
	walletID := uuid.New()
	playerID := "sqs-player-" + walletID.String()
	if err := service.OpenWallet(ctx, walletID, playerID, moneyMustTest("100.00", "BRL"), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	messageID := "sqs-integration-" + uuid.New().String()
	providerID := "provider-sqs-" + uuid.New().String()
	externalID := "external-" + messageID
	body := messageBody(messageID, providerID, externalID, "idem-"+messageID, playerID, walletID, "25.00")
	checkingQueue := &completionCheckingQueue{
		Queue:        queue,
		inbox:        postgres.NewInboxRepository(db),
		consumerName: "sqs-integration-consumer",
		messageID:    messageID,
	}
	consumer := NewConsumer(checkingQueue, service, Config{
		ConsumerName:           "sqs-integration-consumer",
		VisibilityTimeout:      2 * time.Second,
		WaitTime:               time.Second,
		RetryVisibilityBackoff: 0,
		Metrics:                metrics,
	})
	if err := consumer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		if err := consumer.Stop(stopCtx); err != nil {
			t.Errorf("stop consumer: %v", err)
		}
	})
	if err := sendFIFO(ctx, fixture.api, topology.mainURL, body, walletID.String(), messageID+"-first"); err != nil {
		t.Fatal(err)
	}

	transactions := postgres.NewWagerTransactionRepository(db)
	var transaction postgres.WagerTransactionRecord
	if err := eventually(ctx, func() (bool, error) {
		record, findErr := transactions.FindByExternal(ctx, providerID, externalID)
		if errors.Is(findErr, pgx.ErrNoRows) {
			return false, nil
		}
		if findErr != nil {
			return false, findErr
		}
		transaction = record
		return record.State == string(wager.Processed), nil
	}); err != nil {
		t.Fatalf("wait for first SQS financial transaction: %v", err)
	}
	inbox, err := postgres.NewInboxRepository(db).Find(ctx, "sqs-integration-consumer", messageID)
	if err != nil || inbox.CompletedAt == nil {
		t.Fatalf("SQS inbox record = %+v, error=%v", inbox, err)
	}
	if count, countErr := postgres.NewLedgerRepository(db).CountByTransaction(ctx, transaction.ID); countErr != nil || count != 1 {
		t.Fatalf("first SQS ledger entries = %d, error=%v", count, countErr)
	}
	if count, countErr := postgres.NewOutboxRepository(db).CountByTypeAndAggregate(ctx, "WagerTransactionProcessed", transaction.ID); countErr != nil || count != 1 {
		t.Fatalf("first SQS outbox events = %d, error=%v", count, countErr)
	}
	if err := eventually(ctx, func() (bool, error) { return checkingQueue.DeleteCalls() == 1, nil }); err != nil {
		t.Fatalf("wait for first delivery deletion: %v", err)
	}

	// A distinct SQS deduplication ID forces a broker delivery of the same
	// application message ID; the durable inbox must turn it into a replay.
	if err := sendFIFO(ctx, fixture.api, topology.mainURL, body, walletID.String(), messageID+"-replay"); err != nil {
		t.Fatal(err)
	}
	if err := eventually(ctx, func() (bool, error) { return checkingQueue.DeleteCalls() == 2, nil }); err != nil {
		t.Fatalf("wait for duplicate delivery deletion: %v", err)
	}
	if count, countErr := transactions.CountByExternal(ctx, providerID, externalID); countErr != nil || count != 1 {
		t.Fatalf("duplicate SQS transaction count = %d, error=%v", count, countErr)
	}
	if count, countErr := postgres.NewLedgerRepository(db).CountByTransaction(ctx, transaction.ID); countErr != nil || count != 1 {
		t.Fatalf("duplicate SQS ledger entries = %d, error=%v", count, countErr)
	}
	if got := metrics.Render(); !strings.Contains(got, `wager_duplicate_total{kind="sqs_inbox"}`) {
		t.Fatalf("SQS redelivery was not observed through the real consumer path: %s", got)
	}
	if wallet, walletErr := postgres.NewWalletRepository(db).Find(ctx, walletID); walletErr != nil || wallet.Balance != 7500 {
		t.Fatalf("duplicate SQS wallet = %+v, error=%v", wallet, walletErr)
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	if err := consumer.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	remaining, err := fixture.api.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{
		QueueUrl:            aws.String(topology.mainURL),
		MaxNumberOfMessages: 1,
		WaitTimeSeconds:     1,
	})
	if err != nil || len(remaining.Messages) != 0 {
		t.Fatalf("message remained after durable commit and successful delete: messages=%+v error=%v", remaining.Messages, err)
	}
}

func TestSQSConsumerRedeliveryAfterDeleteFailurePreservesOneMovement(t *testing.T) {
	fixture := requireLocalStack(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	topology := createSQSIntegrationTopology(ctx, t, fixture.api, 1, 3)
	fixture.config.SQSWagerQueue = topology.mainName
	fixture.config.SQSWagerDLQ = topology.dlqName
	queue, err := sqsinfrastructure.NewClient(ctx, fixture.config)
	if err != nil {
		t.Fatal(err)
	}
	db, err := postgres.NewRepository(ctx, fixture.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := financial.NewService(db)
	walletID := uuid.New()
	playerID := "sqs-delete-failure-player-" + walletID.String()
	if err := service.OpenWallet(ctx, walletID, playerID, moneyMustTest("100.00", "BRL"), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	messageID := "sqs-delete-failure-" + uuid.New().String()
	providerID := "provider-sqs-delete-failure-" + uuid.New().String()
	externalID := "external-" + messageID
	body := messageBody(messageID, providerID, externalID, "idem-"+messageID, playerID, walletID, "25.00")
	failedDeleteQueue := &failFirstDeleteQueue{Queue: queue}
	consumer := NewConsumer(failedDeleteQueue, service, Config{
		ConsumerName:           "sqs-delete-failure-consumer",
		VisibilityTimeout:      time.Second,
		WaitTime:               time.Second,
		RetryVisibilityBackoff: 0,
	})
	if err := consumer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		if err := consumer.Stop(stopCtx); err != nil {
			t.Errorf("stop consumer: %v", err)
		}
	})
	if err := sendFIFO(ctx, fixture.api, topology.mainURL, body, walletID.String(), messageID); err != nil {
		t.Fatal(err)
	}
	if err := eventually(ctx, func() (bool, error) { return failedDeleteQueue.SuccessfulDeletes() == 1, nil }); err != nil {
		t.Fatalf("wait for redelivery after injected delete failure: %v", err)
	}
	if failedDeleteQueue.DeleteCalls() < 2 {
		t.Fatalf("delete attempts = %d, want failed initial delete plus replay delete", failedDeleteQueue.DeleteCalls())
	}
	transaction, err := postgres.NewWagerTransactionRepository(db).FindByExternal(ctx, providerID, externalID)
	if err != nil || transaction.State != string(wager.Processed) {
		t.Fatalf("redelivered transaction = %+v, error=%v", transaction, err)
	}
	if inbox, findErr := postgres.NewInboxRepository(db).Find(ctx, "sqs-delete-failure-consumer", messageID); findErr != nil || inbox.CompletedAt == nil {
		t.Fatalf("redelivery inbox = %+v, error=%v", inbox, findErr)
	}
	if count, countErr := postgres.NewLedgerRepository(db).CountByTransaction(ctx, transaction.ID); countErr != nil || count != 1 {
		t.Fatalf("redelivery ledger entries = %d, error=%v", count, countErr)
	}
	if count, countErr := postgres.NewWagerTransactionRepository(db).CountByExternal(ctx, providerID, externalID); countErr != nil || count != 1 {
		t.Fatalf("redelivery transactions = %d, error=%v", count, countErr)
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	if err := consumer.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	remaining, err := fixture.api.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{
		QueueUrl:            aws.String(topology.mainURL),
		MaxNumberOfMessages: 1,
		WaitTimeSeconds:     1,
	})
	if err != nil || len(remaining.Messages) != 0 {
		t.Fatalf("redelivery message remained after successful replay delete: messages=%+v error=%v", remaining.Messages, err)
	}
}

func TestTwoSQSConsumersUseOneDurableInboxAcrossInstances(t *testing.T) {
	fixture := requireLocalStack(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	topology := createSQSIntegrationTopology(ctx, t, fixture.api, 5, 3)
	fixture.config.SQSWagerQueue = topology.mainName
	fixture.config.SQSWagerDLQ = topology.dlqName
	queueA, err := sqsinfrastructure.NewClient(ctx, fixture.config)
	if err != nil {
		t.Fatal(err)
	}
	queueB, err := sqsinfrastructure.NewClient(ctx, fixture.config)
	if err != nil {
		t.Fatal(err)
	}
	dbA, err := postgres.NewRepository(ctx, fixture.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer dbA.Close()
	dbB, err := postgres.NewRepository(ctx, fixture.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer dbB.Close()
	serviceA, serviceB := financial.NewService(dbA), financial.NewService(dbB)
	walletID := uuid.New()
	playerID := "sqs-two-consumer-player-" + walletID.String()
	if err := serviceA.OpenWallet(ctx, walletID, playerID, moneyMustTest("100.00", "BRL"), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	messageID := "sqs-two-consumer-" + uuid.New().String()
	providerID := "provider-sqs-two-consumer-" + uuid.New().String()
	externalID := "external-" + messageID
	body := messageBody(messageID, providerID, externalID, "idem-"+messageID, playerID, walletID, "25.00")
	releaseFirst := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(releaseFirst) })
	held := &holdingProcessor{Processor: serviceA, entered: make(chan struct{}), release: releaseFirst}
	consumerA := NewConsumer(queueA, held, Config{
		ConsumerName:           "sqs-two-consumer",
		VisibilityTimeout:      5 * time.Second,
		WaitTime:               time.Second,
		RetryVisibilityBackoff: 0,
	})
	if err := consumerA.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		if err := consumerA.Stop(stopCtx); err != nil {
			t.Errorf("stop first consumer: %v", err)
		}
	})
	if err := sendFIFO(ctx, fixture.api, topology.mainURL, body, walletID.String(), messageID+"-first"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-held.entered:
	case <-ctx.Done():
		t.Fatalf("first consumer did not reach durable processing: %v", ctx.Err())
	}

	// FIFO normally serializes a wallet's group. This deliberately uses a
	// different group only to inject an adversarial duplicate delivery while
	// the first consumer still owns its receipt; normal producer policy remains
	// walletID as MessageGroupId.
	secondQueue := &recordedDeleteQueue{Queue: queueB, deleted: make(chan struct{})}
	consumerB := NewConsumer(secondQueue, serviceB, Config{
		ConsumerName:           "sqs-two-consumer",
		VisibilityTimeout:      5 * time.Second,
		WaitTime:               time.Second,
		RetryVisibilityBackoff: 0,
	})
	if err := consumerB.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		if err := consumerB.Stop(stopCtx); err != nil {
			t.Errorf("stop second consumer: %v", err)
		}
	})
	if err := sendFIFO(ctx, fixture.api, topology.mainURL, body, "adversarial-duplicate-"+uuid.New().String(), messageID+"-second"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-secondQueue.deleted:
	case <-ctx.Done():
		t.Fatalf("second consumer did not delete the durable replay: %v", ctx.Err())
	}
	releaseOnce.Do(func() { close(releaseFirst) })

	transaction, err := postgres.NewWagerTransactionRepository(dbA).FindByExternal(ctx, providerID, externalID)
	if err != nil || transaction.State != string(wager.Processed) {
		t.Fatalf("two-consumer transaction = %+v, error=%v", transaction, err)
	}
	if count, countErr := postgres.NewWagerTransactionRepository(dbA).CountByExternal(ctx, providerID, externalID); countErr != nil || count != 1 {
		t.Fatalf("two-consumer transaction count = %d, error=%v", count, countErr)
	}
	if count, countErr := postgres.NewLedgerRepository(dbA).CountByTransaction(ctx, transaction.ID); countErr != nil || count != 1 {
		t.Fatalf("two-consumer ledger entries = %d, error=%v", count, countErr)
	}
	if inbox, findErr := postgres.NewInboxRepository(dbA).Find(ctx, "sqs-two-consumer", messageID); findErr != nil || inbox.CompletedAt == nil {
		t.Fatalf("two-consumer inbox = %+v, error=%v", inbox, findErr)
	}
}

type neverCalledProcessor struct{}

func (neverCalledProcessor) ProcessMessage(context.Context, string, string, string, financial.Command, time.Time) (financial.Result, bool, error) {
	return financial.Result{}, false, errors.New("malformed SQS body must not reach the financial processor")
}

func TestMalformedSQSMessageRedrivesToLocalStackDLQ(t *testing.T) {
	fixture := requireLocalStack(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	topology := createSQSIntegrationTopology(ctx, t, fixture.api, 1, 2)
	fixture.config.SQSWagerQueue = topology.mainName
	fixture.config.SQSWagerDLQ = topology.dlqName
	queue, err := sqsinfrastructure.NewClient(ctx, fixture.config)
	if err != nil {
		t.Fatal(err)
	}
	metrics := observability.NewMetrics()
	consumer := NewConsumer(queue, neverCalledProcessor{}, Config{
		ConsumerName:           "sqs-dlq-consumer",
		VisibilityTimeout:      time.Second,
		WaitTime:               time.Second,
		RetryVisibilityBackoff: 0,
		MaxReceiveCount:        2,
		Metrics:                metrics,
	})
	if err := consumer.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		if err := consumer.Stop(stopCtx); err != nil {
			t.Errorf("stop consumer: %v", err)
		}
	})
	if err := sendFIFO(ctx, fixture.api, topology.mainURL, `{"messageId":"bad"}`, "malformed-group", "malformed-"+uuid.New().String()); err != nil {
		t.Fatal(err)
	}
	var redriven bool
	if err := eventually(ctx, func() (bool, error) {
		messages, receiveErr := fixture.api.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{
			QueueUrl:            aws.String(topology.dlqURL),
			MaxNumberOfMessages: 1,
			WaitTimeSeconds:     1,
		})
		if receiveErr != nil {
			return false, receiveErr
		}
		redriven = len(messages.Messages) == 1 && aws.ToString(messages.Messages[0].Body) == `{"messageId":"bad"}`
		return redriven, nil
	}); err != nil {
		t.Fatalf("wait for LocalStack DLQ redrive: %v", err)
	}
	if !redriven {
		t.Fatal("malformed message was not redriven")
	}
	if got := metrics.Render(); !strings.Contains(got, "wager_sqs_redrive_candidate_total") {
		t.Fatalf("receive exhaustion candidate was not observed: %s", got)
	}
}

func moneyMustTest(amount, currency string) money.Money {
	value, err := money.New(amount, currency)
	if err != nil {
		panic(err)
	}
	return value
}
