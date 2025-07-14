package mq

import (
	"context"
	"fmt"
	"job-scheduler/pkg/job"
	"os"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

type Client struct {
	conn *amqp.Connection
	ch   *amqp.Channel
}

const (
	JobsExchange    = "jobs.exchange"
	DLXExchange     = "jobs.dlx"
	RetryExchange   = "jobs.retry.exchange"
	DeadLetterQueue = "jobs.dead_letter.queue"
)

func New() (*Client, error) {
	conn, err := amqp.Dial(os.Getenv("RABBITMQ_URL"))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to RabbitMQ: %w", err)
	}

	ch, err := conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("failed to open a channel: %w", err)
	}

	return &Client{conn: conn, ch: ch}, nil
}

// SetupTopology declares all necessary exchanges and queues. Idempotent.
func (c *Client) SetupTopology() error {
	// Main exchange for jobs
	if err := c.ch.ExchangeDeclare(JobsExchange, "direct", true, false, false, false, nil); err != nil {
		return err
	}
	// Dead-letter exchange
	if err := c.ch.ExchangeDeclare(DLXExchange, "fanout", true, false, false, false, nil); err != nil {
		return err
	}
	// Retry exchange
	if err := c.ch.ExchangeDeclare(RetryExchange, "direct", true, false, false, false, nil); err != nil {
		return err
	}

	// Dead-letter queue
	_, err := c.ch.QueueDeclare(DeadLetterQueue, true, false, false, false, nil)
	if err != nil {
		return err
	}
	if err := c.ch.QueueBind(DeadLetterQueue, "", DLXExchange, false, nil); err != nil {
		return err
	}

	// Main job queues (one per job type for isolation)
	jobTypes := []job.Type{job.TypeSendEmail, job.TypeExportData}
	for _, jt := range jobTypes {
		queueName := fmt.Sprintf("jobs.queue.%s", jt)
		_, err := c.ch.QueueDeclare(queueName, true, false, false, false, amqp.Table{
			"x-dead-letter-exchange": DLXExchange, // Failed jobs go to DLX
			"x-max-priority":         int32(10),   // Enable priority levels (0-10)
		})
		if err != nil {
			return err
		}
		if err := c.ch.QueueBind(queueName, string(jt), JobsExchange, false, nil); err != nil {
			return err
		}
	}

	// Retry queues with TTL
	retryDelays := []time.Duration{5 * time.Second, 30 * time.Second, 5 * time.Minute}
	for _, delay := range retryDelays {
		queueName := fmt.Sprintf("jobs.retry.queue.%ds", int(delay.Seconds()))
		routingKey := fmt.Sprintf("retry.%ds", int(delay.Seconds()))
		_, err := c.ch.QueueDeclare(queueName, true, false, false, false, amqp.Table{
			"x-dead-letter-exchange":    JobsExchange, // After TTL, send back to main jobs exchange
			"x-message-ttl":             int64(delay.Milliseconds()),
			"x-dead-letter-routing-key": "", // This will be set per-message
		})
		if err != nil {
			return err
		}
		if err := c.ch.QueueBind(queueName, routingKey, RetryExchange, false, nil); err != nil {
			return err
		}
	}

	return nil
}

// mapPriority converts our semantic priority to RabbitMQ uint8 priority levels.
func mapPriority(p job.Priority) uint8 {
	switch p {
	case job.PriorityHigh:
		return 9
	case job.PriorityNormal:
		return 5
	case job.PriorityLow:
		return 1
	default:
		return 5
	}
}

// PublishJob publishes a job message with the appropriate priority.
func (c *Client) PublishJob(ctx context.Context, j *job.Job) error {
	return c.ch.PublishWithContext(ctx,
		JobsExchange,   // exchange
		string(j.Type), // routing key (matches job type)
		false,          // mandatory
		false,          // immediate
		amqp.Publishing{
			ContentType: "text/plain",
			Body:        []byte(j.ID),
			Priority:    mapPriority(j.Priority),
		})
}

func (c *Client) PublishToRetry(ctx context.Context, jobType job.Type, jobID string, delay time.Duration) error {
	routingKey := fmt.Sprintf("retry.%ds", int(delay.Seconds()))
	return c.ch.PublishWithContext(ctx,
		RetryExchange, // exchange
		routingKey,    // routing key
		false,
		false,
		amqp.Publishing{
			ContentType: "text/plain",
			Body:        []byte(jobID),
			// This tells RabbitMQ where to route the message after the TTL expires
			Headers: amqp.Table{"x-dead-letter-routing-key": string(jobType)},
		})
}


func (c *Client) ConsumeJobs(jobType job.Type) (<-chan amqp.Delivery, error) {
	queueName := fmt.Sprintf("jobs.queue.%s", jobType)
	return c.ch.Consume(
		queueName,
		"",    // consumer
		false, // auto-ack is false. We will manually ack.
		false,
		false,
		false,
		nil,
	)
}

func (c *Client) Close() {
	c.ch.Close()
	c.conn.Close()
} 