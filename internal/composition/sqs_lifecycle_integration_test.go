package composition

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"github.com/leonardodacosta/distributedBettingProcessing/internal/application/financial"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/domain/money"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/domain/wager"
	"github.com/leonardodacosta/distributedBettingProcessing/internal/infrastructure/postgres"
	transporthttp "github.com/leonardodacosta/distributedBettingProcessing/internal/transport/http"
)

func TestFxApplicationConsumerSurvivesStartupContextAndStopsPolling(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	endpoint := os.Getenv("AWS_ENDPOINT_URL")
	if databaseURL == "" || endpoint == "" {
		t.Skip("DATABASE_URL and AWS_ENDPOINT_URL are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	region := valueOrIntegration(os.Getenv("AWS_REGION"), "us-east-1")
	accessKeyID := valueOrIntegration(os.Getenv("AWS_ACCESS_KEY_ID"), "test")
	secretAccessKey := valueOrIntegration(os.Getenv("AWS_SECRET_ACCESS_KEY"), "test")
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, "")),
	)
	if err != nil {
		t.Fatal(err)
	}
	awsCfg.BaseEndpoint = aws.String(endpoint)
	api := awssqs.NewFromConfig(awsCfg)
	queueName, queueURL := createFxIntegrationQueue(ctx, t, api)

	for key, value := range map[string]string{
		"DATABASE_URL":                         databaseURL,
		"AWS_REGION":                           region,
		"AWS_ENDPOINT_URL":                     endpoint,
		"AWS_ACCESS_KEY_ID":                    accessKeyID,
		"AWS_SECRET_ACCESS_KEY":                secretAccessKey,
		"SQS_WAGER_QUEUE":                      queueName,
		"SQS_WAGER_DLQ":                        "unused-" + uuid.New().String() + ".fifo",
		"SQS_VISIBILITY_TIMEOUT_SECONDS":       "2",
		"SQS_WAIT_TIME_SECONDS":                "1",
		"SQS_RETRY_VISIBILITY_BACKOFF_SECONDS": "0",
		"SQS_MAX_MESSAGES":                     "1",
		"HTTP_ADDR":                            "127.0.0.1:0",
		"HTTP_SHUTDOWN_TIMEOUT":                "5s",
		"KEYCLOAK_REALM":                       "wagering",
		"OIDC_ISSUER":                          "http://localhost:8082/realms/wagering",
		"OIDC_JWKS_URL":                        "http://localhost:8082/realms/wagering/protocol/openid-connect/certs",
		"OAUTH_AUDIENCE":                       "wagering-api",
		"LOG_LEVEL":                            "error",
		"OUTBOX_ENABLED":                       "false",
	} {
		t.Setenv(key, value)
	}

	db, err := postgres.NewRepository(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := financial.NewService(db)
	walletID := uuid.New()
	playerID := "fx-sqs-player-" + walletID.String()
	if err := service.OpenWallet(ctx, walletID, playerID, integrationMoney("100.00", "BRL"), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	var server *transporthttp.Server
	app := fx.New(Module(), fx.Populate(&server), fx.NopLogger)
	started := false
	t.Cleanup(func() {
		if !started {
			return
		}
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		if err := app.Stop(stopCtx); err != nil {
			t.Errorf("stop Fx application: %v", err)
		}
	})
	startupCtx, cancelStartup := context.WithTimeout(ctx, 5*time.Second)
	if err := app.Start(startupCtx); err != nil {
		cancelStartup()
		t.Fatal(err)
	}
	// Fx.Run cancels this context immediately after App.Start succeeds. Doing
	// so here makes the former lifecycle defect observable in this real app.
	cancelStartup()
	started = true
	if server == nil || server.Addr() == "" {
		t.Fatal("Fx did not expose a listening HTTP server")
	}
	response, err := (&http.Client{Timeout: 3 * time.Second}).Get("http://" + server.Addr() + "/health/ready")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var ready struct {
		Status string            `json:"status"`
		Checks map[string]string `json:"checks"`
	}
	if err := json.NewDecoder(response.Body).Decode(&ready); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || ready.Status != "UP" || ready.Checks["postgres"] != "UP" || ready.Checks["sqs"] != "UP" {
		t.Fatalf("readiness status=%d body=%+v", response.StatusCode, ready)
	}

	messageID := "fx-sqs-message-" + uuid.New().String()
	providerID := "fx-sqs-provider-" + uuid.New().String()
	externalID := "fx-sqs-external-" + uuid.New().String()
	body := fxMessageBody(messageID, providerID, externalID, "fx-sqs-idempotency-"+uuid.New().String(), playerID, walletID)
	if err := sendFxFIFO(ctx, api, queueURL, body, walletID.String(), messageID); err != nil {
		t.Fatal(err)
	}
	transactions := postgres.NewWagerTransactionRepository(db)
	var transaction postgres.WagerTransactionRecord
	if err := eventuallyFx(ctx, func() (bool, error) {
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
		t.Fatalf("wait for Fx SQS transaction: %v", err)
	}
	inbox, err := postgres.NewInboxRepository(db).Find(ctx, "wager-transaction-consumer", messageID)
	if err != nil || inbox.CompletedAt == nil {
		t.Fatalf("Fx inbox record = %+v, error=%v", inbox, err)
	}
	if count, countErr := postgres.NewLedgerRepository(db).CountByTransaction(ctx, transaction.ID); countErr != nil || count != 1 {
		t.Fatalf("Fx ledger entries = %d, error=%v", count, countErr)
	}
	if count, countErr := postgres.NewOutboxRepository(db).CountByTypeAndAggregate(ctx, "WagerTransactionProcessed", transaction.ID); countErr != nil || count != 1 {
		t.Fatalf("Fx outbox events = %d, error=%v", count, countErr)
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := app.Stop(stopCtx); err != nil {
		stopCancel()
		t.Fatal(err)
	}
	stopCancel()
	started = false

	stoppedMessageID := "fx-sqs-after-stop-" + uuid.New().String()
	stoppedExternalID := "fx-sqs-after-stop-external-" + uuid.New().String()
	stoppedBody := fxMessageBody(stoppedMessageID, providerID, stoppedExternalID, "fx-sqs-after-stop-idempotency-"+uuid.New().String(), playerID, walletID)
	if err := sendFxFIFO(ctx, api, queueURL, stoppedBody, walletID.String(), stoppedMessageID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	if _, err := transactions.FindByExternal(ctx, providerID, stoppedExternalID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("consumer processed a message after Stop: %v", err)
	}
	if _, err := postgres.NewInboxRepository(db).Find(ctx, "wager-transaction-consumer", stoppedMessageID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("consumer created inbox after Stop: %v", err)
	}
	if err := eventuallyFx(ctx, func() (bool, error) {
		remaining, receiveErr := api.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{
			QueueUrl:            aws.String(queueURL),
			MaxNumberOfMessages: 1,
			WaitTimeSeconds:     1,
		})
		if receiveErr != nil {
			return false, receiveErr
		}
		if len(remaining.Messages) == 0 {
			return false, nil
		}
		if aws.ToString(remaining.Messages[0].Body) != stoppedBody {
			return false, fmt.Errorf("unexpected message after Stop: %q", aws.ToString(remaining.Messages[0].Body))
		}
		return true, nil
	}); err != nil {
		t.Fatalf("message after Stop was not left for redelivery: %v", err)
	}
}

func TestFxApplicationOutboxWorkerUsesClaimOneAfterStartup(t *testing.T) {
	if os.Getenv("RUN_OUTBOX_FX_INTEGRATION") != "1" {
		t.Skip("RUN_OUTBOX_FX_INTEGRATION=1 is required")
	}
	databaseURL := os.Getenv("DATABASE_URL")
	endpoint := os.Getenv("AWS_ENDPOINT_URL")
	if databaseURL == "" || endpoint == "" {
		t.Skip("DATABASE_URL and AWS_ENDPOINT_URL are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	region := valueOrIntegration(os.Getenv("AWS_REGION"), "us-east-1")
	accessKeyID := valueOrIntegration(os.Getenv("AWS_ACCESS_KEY_ID"), "test")
	secretAccessKey := valueOrIntegration(os.Getenv("AWS_SECRET_ACCESS_KEY"), "test")
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, "")),
	)
	if err != nil {
		t.Fatal(err)
	}
	awsCfg.BaseEndpoint = aws.String(endpoint)
	api := awssqs.NewFromConfig(awsCfg)
	queue, err := api.GetQueueUrl(ctx, &awssqs.GetQueueUrlInput{QueueName: aws.String("wager-events.fifo")})
	if err != nil {
		t.Fatalf("get outbox queue: %v", err)
	}

	for key, value := range map[string]string{
		"DATABASE_URL":                         databaseURL,
		"AWS_REGION":                           region,
		"AWS_ENDPOINT_URL":                     endpoint,
		"AWS_ACCESS_KEY_ID":                    accessKeyID,
		"AWS_SECRET_ACCESS_KEY":                secretAccessKey,
		"SQS_WAGER_QUEUE":                      "unused-" + uuid.New().String() + ".fifo",
		"SQS_WAGER_DLQ":                        "unused-dlq-" + uuid.New().String() + ".fifo",
		"SQS_EVENT_QUEUE":                      "wager-events.fifo",
		"SQS_VISIBILITY_TIMEOUT_SECONDS":       "2",
		"SQS_WAIT_TIME_SECONDS":                "1",
		"SQS_RETRY_VISIBILITY_BACKOFF_SECONDS": "0",
		"SQS_MAX_MESSAGES":                     "1",
		"OUTBOX_ENABLED":                       "true",
		"OUTBOX_POLL_INTERVAL":                 "10ms",
		"OUTBOX_BATCH_SIZE":                    "1",
		"OUTBOX_BACKOFF":                       "0s",
		"OUTBOX_CLAIM_LEASE":                   "1s",
		"HTTP_ADDR":                            "127.0.0.1:0",
		"HTTP_SHUTDOWN_TIMEOUT":                "5s",
		"KEYCLOAK_REALM":                       "wagering",
		"OIDC_ISSUER":                          "http://localhost:8082/realms/wagering",
		"OIDC_JWKS_URL":                        "http://localhost:8082/realms/wagering/protocol/openid-connect/certs",
		"OAUTH_AUDIENCE":                       "wagering-api",
		"LOG_LEVEL":                            "error",
	} {
		t.Setenv(key, value)
	}

	isolationPool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	isolationTx, err := isolationPool.Begin(ctx)
	if err != nil {
		isolationPool.Close()
		t.Fatal(err)
	}
	defer isolationPool.Close()
	defer isolationTx.Rollback(ctx)
	if _, err := isolationTx.Exec(ctx, `UPDATE outbox SET next_attempt_at=next_attempt_at`); err != nil {
		t.Fatal(err)
	}

	writer, err := postgres.NewRepository(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	var server *transporthttp.Server
	app := fx.New(Module(), fx.Populate(&server), fx.NopLogger)
	started := false
	t.Cleanup(func() {
		if started {
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer stopCancel()
			if err := app.Stop(stopCtx); err != nil {
				t.Errorf("stop Fx application: %v", err)
			}
		}
	})
	startupCtx, cancelStartup := context.WithTimeout(ctx, 5*time.Second)
	if err := app.Start(startupCtx); err != nil {
		cancelStartup()
		t.Fatal(err)
	}
	cancelStartup()
	started = true

	walletID := uuid.New()
	if err := financial.NewService(writer).OpenWallet(ctx, walletID, "fx-outbox-player-"+walletID.String(), integrationMoney("25.00", "BRL"), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	outbox := postgres.NewOutboxRepository(writer)
	var event postgres.OutboxRecord
	if err := eventuallyFx(ctx, func() (bool, error) {
		record, findErr := outbox.FindFirstByTypeAndAggregate(ctx, "WalletBalanceChanged", walletID)
		if errors.Is(findErr, pgx.ErrNoRows) {
			return false, nil
		}
		if findErr != nil {
			return false, findErr
		}
		event = record
		return record.Status == "PUBLISHED", nil
	}); err != nil {
		t.Fatalf("wait for Fx outbox publication through ClaimOneRecord: %v", err)
	}

	if err := eventuallyFx(ctx, func() (bool, error) {
		received, receiveErr := api.ReceiveMessage(ctx, &awssqs.ReceiveMessageInput{
			QueueUrl:            queue.QueueUrl,
			MaxNumberOfMessages: 1,
			WaitTimeSeconds:     1,
		})
		if receiveErr != nil {
			return false, receiveErr
		}
		for _, message := range received.Messages {
			if strings.Contains(aws.ToString(message.Body), event.EventID.String()) {
				if _, deleteErr := api.DeleteMessage(ctx, &awssqs.DeleteMessageInput{QueueUrl: queue.QueueUrl, ReceiptHandle: message.ReceiptHandle}); deleteErr != nil {
					return false, deleteErr
				}
				return true, nil
			}
		}
		return false, nil
	}); err != nil {
		t.Fatalf("outbox event was not received from LocalStack: %v", err)
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	if err := app.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	started = false
}

func createFxIntegrationQueue(ctx context.Context, t *testing.T, api *awssqs.Client) (string, string) {
	t.Helper()
	name := "fx-loop7-" + uuid.New().String() + ".fifo"
	queue, err := api.CreateQueue(ctx, &awssqs.CreateQueueInput{
		QueueName: aws.String(name),
		Attributes: map[string]string{
			"FifoQueue":                 "true",
			"ContentBasedDeduplication": "true",
			"VisibilityTimeout":         "2",
		},
	})
	if err != nil || queue.QueueUrl == nil {
		t.Fatalf("create Fx integration queue: url=%v error=%v", queue.QueueUrl, err)
	}
	queueURL := *queue.QueueUrl
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := api.DeleteQueue(cleanupCtx, &awssqs.DeleteQueueInput{QueueUrl: aws.String(queueURL)}); err != nil {
			t.Logf("delete temporary Fx queue %q: %v", name, err)
		}
	})
	return name, queueURL
}

func fxMessageBody(messageID, providerID, externalID, idempotencyKey, playerID string, walletID uuid.UUID) string {
	return `{"messageId":"` + messageID + `","type":"WagerTransactionRequested","occurredAt":"2026-09-08T12:00:00.000Z","data":{"providerId":"` + providerID + `","externalTransactionId":"` + externalID + `","idempotencyKey":"` + idempotencyKey + `","playerId":"` + playerID + `","walletId":"` + walletID.String() + `","roundId":"fx-round","gameId":"fx-game","kind":"BET","money":{"amount":"25.00","currency":"BRL"}}}`
}

func sendFxFIFO(ctx context.Context, api *awssqs.Client, queueURL, body, groupID, deduplicationID string) error {
	_, err := api.SendMessage(ctx, &awssqs.SendMessageInput{
		QueueUrl:               aws.String(queueURL),
		MessageBody:            aws.String(body),
		MessageGroupId:         aws.String(groupID),
		MessageDeduplicationId: aws.String(deduplicationID),
	})
	return err
}

func eventuallyFx(ctx context.Context, fn func() (bool, error)) error {
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

func integrationMoney(amount, currency string) money.Money {
	value, err := money.New(amount, currency)
	if err != nil {
		panic(fmt.Sprintf("invalid integration fixture money: %v", err))
	}
	return value
}

func valueOrIntegration(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
