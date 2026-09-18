#!/bin/sh
set -eu

MAX_RECEIVE_COUNT="${SQS_MAX_RECEIVE_COUNT:-5}"

echo "Creating SQS dead-letter queue..."

DLQ_URL="$(
  awslocal sqs create-queue \
    --queue-name wager-transactions-dlq.fifo \
    --attributes '{"FifoQueue":"true","ContentBasedDeduplication":"true"}' \
    --query QueueUrl \
    --output text
)"

DLQ_ARN="$(
  awslocal sqs get-queue-attributes \
    --queue-url "$DLQ_URL" \
    --attribute-names QueueArn \
    --query 'Attributes.QueueArn' \
    --output text
)"

echo "Creating SQS wagering queue..."

awslocal sqs create-queue \
  --queue-name wager-transactions.fifo \
  --attributes "{\"FifoQueue\":\"true\",\"ContentBasedDeduplication\":\"true\",\"RedrivePolicy\":\"{\\\"deadLetterTargetArn\\\":\\\"${DLQ_ARN}\\\",\\\"maxReceiveCount\\\":\\\"${MAX_RECEIVE_COUNT}\\\"}\"}"

echo "SQS queues initialized successfully."
