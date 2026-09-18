# SQS least-privilege access policy

This is deployment policy for the Loop 7 broker adapter. It is not a domain
rule and it must be applied by the AWS/IAM provisioning responsibility, not by
financial application code.

The deployment substitutes `${AWS_REGION}` and `${AWS_ACCOUNT_ID}` in the
following queue ARNs:

```text
arn:aws:sqs:${AWS_REGION}:${AWS_ACCOUNT_ID}:wager-transactions.fifo
arn:aws:sqs:${AWS_REGION}:${AWS_ACCOUNT_ID}:wager-transactions-dlq.fifo
```

## Runtime consumer

The application consumer role receives only these actions on the main queue:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "ConsumeWagerTransactions",
      "Effect": "Allow",
      "Action": [
        "sqs:GetQueueUrl",
        "sqs:GetQueueAttributes",
        "sqs:ReceiveMessage",
        "sqs:DeleteMessage",
        "sqs:ChangeMessageVisibility"
      ],
      "Resource": "arn:aws:sqs:${AWS_REGION}:${AWS_ACCOUNT_ID}:wager-transactions.fifo"
    }
  ]
}
```

`GetQueueAttributes` supports readiness. `ChangeMessageVisibility` supports
retry backoff and safe release during shutdown. The consumer gets neither
`SendMessage`, queue creation/configuration, nor DLQ access. Broker redrive is
performed from the configured redrive policy; it does not require the consumer
to send to the DLQ.

## Producer, test and provisioning responsibilities

These responsibilities use distinct identities from the runtime consumer:

- a producer has `sqs:GetQueueUrl` and `sqs:SendMessage` on the main queue;
- an operational DLQ role receives `GetQueueUrl`, `GetQueueAttributes`,
  `ReceiveMessage`, `DeleteMessage` and `ChangeMessageVisibility` only when it
  is responsible for inspecting or replaying DLQ messages;
- the infrastructure provisioner creates the FIFO queues and applies
  `FifoQueue`, visibility, content-deduplication and redrive attributes. It is
  the only role permitted to create queues or change queue attributes;
- integration-test credentials may create, configure and delete uniquely named
  ephemeral test queues. They are not production application credentials.

The provisioner policy must be scoped to the deployment's approved queue names
and account/region. It may require broader `CreateQueue` permission because a
queue ARN does not exist before creation; that exception belongs to the
provisioning identity, never to the application consumer.

## LocalStack evidence boundary

The local Compose stack uses LocalStack test credentials to exercise queue
topology and broker operations. It does **not** demonstrate faithful AWS IAM
identity-policy or queue-policy enforcement. AWS IAM enforcement remains a
deployment verification responsibility and must not be inferred from passing
LocalStack tests.
