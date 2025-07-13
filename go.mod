module job-scheduler

go 1.21

require (
	github.com/jackc/pgx/v5 v5.5.4
	github.com/prometheus/client_golang v1.17.0
	github.com/rabbitmq/amqp091-go v1.10.0
)

replace github.com/rabbitmq/amqp091-go v1.10.1 => github.com/rabbitmq/amqp091-go v1.10.0 