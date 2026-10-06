#!/usr/bin/env python3
"""Provision incoming commands/DLQ and a separate outgoing domain-event FIFO."""
import json
import os
import boto3

region = os.environ.get("AWS_DEFAULT_REGION", "us-east-1")
sqs = boto3.client("sqs", endpoint_url="http://localhost:4566", region_name=region,
                   aws_access_key_id="test", aws_secret_access_key="test")
dlq = sqs.create_queue(QueueName="wager-transactions-dlq.fifo", Attributes={
    "FifoQueue": "true", "ContentBasedDeduplication": "false",
    "MessageRetentionPeriod": "1209600"})["QueueUrl"]
dlq_arn = sqs.get_queue_attributes(QueueUrl=dlq, AttributeNames=["QueueArn"])["Attributes"]["QueueArn"]
main = sqs.create_queue(QueueName="wager-transactions.fifo", Attributes={
    "FifoQueue": "true", "ContentBasedDeduplication": "false",
    "VisibilityTimeout": os.environ.get("SQS_VISIBILITY_SECONDS", "90"),
    "ReceiveMessageWaitTimeSeconds": "20",
    "RedrivePolicy": json.dumps({"deadLetterTargetArn": dlq_arn,
                                  "maxReceiveCount": os.environ.get("SQS_MAX_RECEIVE_COUNT", "5")})})["QueueUrl"]
main_arn = sqs.get_queue_attributes(QueueUrl=main, AttributeNames=["QueueArn"])["Attributes"]["QueueArn"]
sqs.set_queue_attributes(QueueUrl=dlq, Attributes={"RedriveAllowPolicy": json.dumps({
    "redrivePermission": "byQueue", "sourceQueueArns": [main_arn]})})
sqs.create_queue(QueueName="wager-events.fifo", Attributes={
    "FifoQueue": "true", "ContentBasedDeduplication": "false",
    "ReceiveMessageWaitTimeSeconds": "20", "MessageRetentionPeriod": "1209600"})
print("Provisioned command FIFO, command DLQ and domain-event FIFO")
