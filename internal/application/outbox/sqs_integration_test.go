package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/leonardodacosta/distributedBettingProcessing/internal/application/financial"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/domain/money"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/infrastructure/config"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/infrastructure/postgres"
	sqsinfrastructure "github.com/leonardodacosta/distributedBettingProcessing/internal/infrastructure/sqs"
)

type failOncePublisher struct {
	delegate EventPublisher
	target   string
	mu       sync.Mutex
	failed   bool
}

func (p *failOncePublisher) SendEvent(ctx context.Context, body, groupID, eventID string) error {
	p.mu.Lock()
	if eventID == p.target && !p.failed {
		p.failed = true
		p.mu.Unlock()
		return context.DeadlineExceeded
	}
	p.mu.Unlock()
	return p.delegate.SendEvent(ctx, body, groupID, eventID)
}

func TestOutboxPublishesToRealLocalStackFIFO(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	endpoint := os.Getenv("AWS_ENDPOINT_URL")
	if databaseURL == "" || endpoint == "" {
		t.Skip("DATABASE_URL and AWS_ENDPOINT_URL are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db := newOutboxTestDB(t)
	defer db.Close()
	eventID := insertOutboxTestEvent(t, db, time.Now().UTC())
	cfg := config.Config{
		AWSRegion: "us-east-1", AWSEndpointURL: endpoint, AWSAccessKeyID: "test", AWSSecretAccessKey: "test", SQSWagerQueue: "wager-transactions.fifo", SQSEventQueue: "wager-events.fifo",
	}
	client, err := sqsinfrastructure.NewClient(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	worker := NewWorker(db, client, Config{ClaimLease: time.Minute})
	if err := processUntilPublished(ctx, worker, db, eventID); err != nil {
		t.Fatal(err)
	}
	queueURL, err := client.EventQueueURL(ctx)
	if err != nil {
		t.Fatal(err)
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion("us-east-1"), awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")))
	if err != nil {
		t.Fatal(err)
	}
	awsCfg.BaseEndpoint = aws.String(endpoint)
	api := awssqs.NewFromConfig(awsCfg)
	found := false
	deadline := time.Now().Add(5 * time.Second)
	for !found && time.Now().Before(deadline) {
		received, receiveErr := api.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{QueueUrl: aws.String(queueURL), MaxNumberOfMessages: 10, WaitTimeSeconds: 1})
		if receiveErr != nil {
			t.Fatal(receiveErr)
		}
		for _, message := range received.Messages {
			if message.Body != nil && contains(aws.ToString(message.Body), eventID.String()) {
				found = true
			}
			if message.ReceiptHandle != nil {
				_, _ = api.DeleteMessage(ctx, &awssqs.DeleteMessageInput{QueueUrl: aws.String(queueURL), ReceiptHandle: message.ReceiptHandle})
			}
		}
	}
	if !found {
		t.Fatalf("event %s was not received from LocalStack FIFO", eventID)
	}
}

func TestWalletBalanceChangedWireContractAgainstRealLocalStack(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	endpoint := os.Getenv("AWS_ENDPOINT_URL")
	if databaseURL == "" || endpoint == "" {
		t.Skip("DATABASE_URL and AWS_ENDPOINT_URL are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db := newOutboxTestDB(t)
	defer db.Close()
	opening, err := money.New("100.00", "BRL")
	if err != nil {
		t.Fatal(err)
	}
	walletID := uuid.New()
	if err := financial.NewService(db).OpenWallet(ctx, walletID, "wire-contract-player-"+walletID.String(), opening, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	record, err := postgres.NewOutboxRepository(db).FindFirstByTypeAndAggregate(ctx, "WalletBalanceChanged", walletID)
	if err != nil {
		t.Fatal(err)
	}
	client, err := sqsinfrastructure.NewClient(ctx, config.Config{AWSRegion: "us-east-1", AWSEndpointURL: endpoint, AWSAccessKeyID: "test", AWSSecretAccessKey: "test", SQSEventQueue: "wager-events.fifo"})
	if err != nil {
		t.Fatal(err)
	}
	worker := NewWorker(db, client, Config{ClaimLease: time.Minute})
	if err := processUntilPublished(ctx, worker, db, record.EventID); err != nil {
		t.Fatal(err)
	}
	queueURL, err := client.EventQueueURL(ctx)
	if err != nil {
		t.Fatal(err)
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion("us-east-1"), awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")))
	if err != nil {
		t.Fatal(err)
	}
	awsCfg.BaseEndpoint = aws.String(endpoint)
	api := awssqs.NewFromConfig(awsCfg)
	var body string
	deadline := time.Now().Add(5 * time.Second)
	for body == "" && time.Now().Before(deadline) {
		received, receiveErr := api.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{QueueUrl: aws.String(queueURL), MaxNumberOfMessages: 10, WaitTimeSeconds: 1})
		if receiveErr != nil {
			t.Fatal(receiveErr)
		}
		for _, message := range received.Messages {
			if message.Body != nil && contains(aws.ToString(message.Body), record.EventID.String()) {
				body = aws.ToString(message.Body)
			}
			if message.ReceiptHandle != nil {
				_, _ = api.DeleteMessage(ctx, &awssqs.DeleteMessageInput{QueueUrl: aws.String(queueURL), ReceiptHandle: message.ReceiptHandle})
			}
		}
	}
	if body == "" {
		t.Fatalf("event %s was not received from LocalStack", record.EventID)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatal(err)
	}
	var eventType string
	if err := json.Unmarshal(envelope["eventType"], &eventType); err != nil || eventType != "WalletBalanceChanged" {
		t.Fatalf("event type = %q, error=%v", eventType, err)
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(envelope["data"], &data); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"walletId", "transactionId", "direction", "money", "balanceBefore", "balanceAfter", "walletVersion"} {
		if _, ok := data[field]; !ok {
			t.Fatalf("missing WalletBalanceChanged field %q: %s", field, body)
		}
	}
	if _, ok := data["amount"]; ok {
		t.Fatalf("legacy amount field present: %s", body)
	}
	assertWireMoney := func(field, expected string) {
		t.Helper()
		var value struct {
			Amount   string `json:"amount"`
			Currency string `json:"currency"`
		}
		if err := json.Unmarshal(data[field], &value); err != nil || value.Amount != expected || value.Currency != "BRL" {
			t.Fatalf("%s = %s, want %s BRL; error=%v", field, data[field], expected, err)
		}
	}
	assertWireMoney("money", "100.00")
	assertWireMoney("balanceBefore", "0.00")
	assertWireMoney("balanceAfter", "100.00")
}

func TestSameAggregateOrderingAfterRealLocalStackRetry(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	endpoint := os.Getenv("AWS_ENDPOINT_URL")
	if databaseURL == "" || endpoint == "" {
		t.Skip("DATABASE_URL and AWS_ENDPOINT_URL are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db := newOutboxTestDB(t)
	defer db.Close()
	aggregateID := uuid.New()
	e1 := insertOutboxTestEventForAggregate(t, db, aggregateID, time.Now().UTC())
	e2 := insertOutboxTestEventForAggregate(t, db, aggregateID, time.Now().UTC())
	client, err := sqsinfrastructure.NewClient(ctx, config.Config{AWSRegion: "us-east-1", AWSEndpointURL: endpoint, AWSAccessKeyID: "test", AWSSecretAccessKey: "test", SQSEventQueue: "wager-events.fifo"})
	if err != nil {
		t.Fatal(err)
	}
	worker := NewWorker(db, &failOncePublisher{delegate: client, target: e1.String()}, Config{Backoff: 0, ClaimLease: time.Minute})
	if err := processUntilPublished(ctx, worker, db, e1); err != nil {
		t.Fatal(err)
	}
	if err := processUntilPublished(ctx, worker, db, e2); err != nil {
		t.Fatal(err)
	}
	queueURL, err := client.EventQueueURL(ctx)
	if err != nil {
		t.Fatal(err)
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion("us-east-1"), awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")))
	if err != nil {
		t.Fatal(err)
	}
	awsCfg.BaseEndpoint = aws.String(endpoint)
	api := awssqs.NewFromConfig(awsCfg)
	seen := make([]string, 0, 2)
	deadline := time.Now().Add(5 * time.Second)
	for len(seen) < 2 && time.Now().Before(deadline) {
		received, receiveErr := api.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{QueueUrl: aws.String(queueURL), MaxNumberOfMessages: 10, WaitTimeSeconds: 1})
		if receiveErr != nil {
			t.Fatal(receiveErr)
		}
		for _, message := range received.Messages {
			if message.Body != nil {
				body := aws.ToString(message.Body)
				if contains(body, e1.String()) {
					seen = append(seen, e1.String())
				} else if contains(body, e2.String()) {
					seen = append(seen, e2.String())
				}
			}
			if message.ReceiptHandle != nil {
				_, _ = api.DeleteMessage(ctx, &awssqs.DeleteMessageInput{QueueUrl: aws.String(queueURL), ReceiptHandle: message.ReceiptHandle})
			}
		}
	}
	if len(seen) != 2 || seen[0] != e1.String() || seen[1] != e2.String() {
		t.Fatalf("same aggregate broker order = %v, want [%s %s]", seen, e1, e2)
	}
}

func TestProductionWorkerClaimOneOrderingAfterRealLocalStackRetry(t *testing.T) {
	requireProductionOutboxIntegration(t)
	databaseURL := os.Getenv("DATABASE_URL")
	endpoint := os.Getenv("AWS_ENDPOINT_URL")
	if databaseURL == "" || endpoint == "" {
		t.Skip("DATABASE_URL and AWS_ENDPOINT_URL are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	db := newOutboxTestDB(t)
	defer db.Close()
	aggregateID := uuid.New()
	e1 := insertOutboxTestEventForAggregate(t, db, aggregateID, time.Now().UTC())
	e2 := insertOutboxTestEventForAggregate(t, db, aggregateID, time.Now().UTC())
	isolationPool, isolationTx := isolateOutboxRows(t, ctx, databaseURL, e1, e2)
	defer isolationPool.Close()
	defer isolationTx.Rollback(ctx)
	setOutboxNextAttempt(t, ctx, databaseURL, e1, time.Unix(0, 0).UTC())
	setOutboxNextAttempt(t, ctx, databaseURL, e2, time.Now().UTC().Add(-time.Minute))
	client, err := sqsinfrastructure.NewClient(ctx, config.Config{AWSRegion: "us-east-1", AWSEndpointURL: endpoint, AWSAccessKeyID: "test", AWSSecretAccessKey: "test", SQSEventQueue: "wager-events.fifo"})
	if err != nil {
		t.Fatal(err)
	}
	worker := NewWorker(db, &failOncePublisher{delegate: client, target: e1.String()}, Config{Backoff: time.Hour, ClaimLease: time.Minute})
	now := time.Now().UTC()
	if _, err := worker.processOne(ctx, now); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.processOne(ctx, now); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("successor bypassed retryable predecessor: %v", err)
	}
	if _, err := worker.processOne(ctx, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.processOne(ctx, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	queueURL, err := client.EventQueueURL(ctx)
	if err != nil {
		t.Fatal(err)
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion("us-east-1"), awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")))
	if err != nil {
		t.Fatal(err)
	}
	awsCfg.BaseEndpoint = aws.String(endpoint)
	api := awssqs.NewFromConfig(awsCfg)
	seen := make([]string, 0, 2)
	deadline := time.Now().Add(8 * time.Second)
	for len(seen) < 2 && time.Now().Before(deadline) {
		received, receiveErr := api.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{QueueUrl: aws.String(queueURL), MaxNumberOfMessages: 10, WaitTimeSeconds: 1})
		if receiveErr != nil {
			t.Fatal(receiveErr)
		}
		for _, message := range received.Messages {
			if message.Body != nil {
				body := aws.ToString(message.Body)
				if contains(body, e1.String()) {
					seen = append(seen, e1.String())
				} else if contains(body, e2.String()) {
					seen = append(seen, e2.String())
				}
			}
			if message.ReceiptHandle != nil {
				_, _ = api.DeleteMessage(ctx, &awssqs.DeleteMessageInput{QueueUrl: aws.String(queueURL), ReceiptHandle: message.ReceiptHandle})
			}
		}
	}
	if len(seen) != 2 || seen[0] != e1.String() || seen[1] != e2.String() {
		t.Fatalf("ClaimOneRecord same-aggregate broker order = %v, want [%s %s]", seen, e1, e2)
	}
}
