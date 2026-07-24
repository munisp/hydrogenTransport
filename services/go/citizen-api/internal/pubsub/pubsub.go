// Package pubsub publishes CloudEvents-ish envelopes (SPEC §3.3) for the
// citizen domain. Preferred transport is the Dapr pubsub building block
// (component "h2pubsub", Kafka-backed) when DAPR_GRPC_PORT is set; otherwise
// it falls back to direct Kafka via franz-go (SPEC §3.5).
package pubsub

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"

	dapr "github.com/dapr/go-sdk/client"
	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kgo"
	"go.uber.org/zap"
)

// Envelope is the CloudEvents-ish event envelope shared by all H2Fleet producers.
type Envelope struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Source string `json:"source"`
	Time   string `json:"time"`
	Data   any    `json:"data"`
}

// Publisher publishes domain events.
type Publisher interface {
	Publish(ctx context.Context, topic string, data any) error
	Close()
}

const source = "citizen-api"

// New selects the transport: Dapr (when DAPR_GRPC_PORT is set), else direct
// Kafka (when KAFKA_BROKERS is set), else a logging no-op publisher.
func New(log *zap.Logger) Publisher {
	if port := os.Getenv("DAPR_GRPC_PORT"); port != "" {
		c, err := dapr.NewClientWithPort(port)
		if err != nil {
			log.Error("dapr client init failed; trying direct kafka", zap.Error(err))
		} else {
			name := os.Getenv("DAPR_PUBSUB_NAME")
			if name == "" {
				name = "h2pubsub"
			}
			log.Info("publishing via dapr pubsub", zap.String("component", name), zap.String("port", port))
			return &daprPublisher{client: c, component: name, log: log}
		}
	}
	if brokers := os.Getenv("KAFKA_BROKERS"); strings.TrimSpace(brokers) != "" {
		c, err := kgo.NewClient(
			kgo.SeedBrokers(strings.Split(brokers, ",")...),
			kgo.RequiredAcks(kgo.AllISRAcks()),
		)
		if err != nil {
			log.Error("kafka client init failed; falling back to no-op publisher", zap.Error(err))
		} else {
			log.Info("publishing via direct kafka", zap.String("brokers", brokers))
			return &kafkaPublisher{client: c, log: log}
		}
	}
	log.Warn("no DAPR_GRPC_PORT or KAFKA_BROKERS set; events will be logged but not published")
	return &noopPublisher{log: log}
}

func marshal(topic string, data any) ([]byte, error) {
	return json.Marshal(Envelope{
		ID:     uuid.NewString(),
		Type:   topic,
		Source: source,
		Time:   time.Now().UTC().Format(time.RFC3339),
		Data:   data,
	})
}

type daprPublisher struct {
	client    dapr.Client
	component string
	log       *zap.Logger
}

func (p *daprPublisher) Publish(ctx context.Context, topic string, data any) error {
	payload, err := marshal(topic, data)
	if err != nil {
		return err
	}
	if err := p.client.PublishEvent(ctx, p.component, topic, payload); err != nil {
		p.log.Error("dapr publish failed", zap.String("topic", topic), zap.Error(err))
		return err
	}
	return nil
}

func (p *daprPublisher) Close() { p.client.Close() }

type kafkaPublisher struct {
	client *kgo.Client
	log    *zap.Logger
}

func (p *kafkaPublisher) Publish(ctx context.Context, topic string, data any) error {
	payload, err := marshal(topic, data)
	if err != nil {
		return err
	}
	if err := p.client.ProduceSync(ctx, &kgo.Record{Topic: topic, Value: payload}).FirstErr(); err != nil {
		p.log.Error("kafka produce failed", zap.String("topic", topic), zap.Error(err))
		return err
	}
	return nil
}

func (p *kafkaPublisher) Close() { p.client.Close() }

type noopPublisher struct {
	log *zap.Logger
}

func (p *noopPublisher) Publish(_ context.Context, topic string, data any) error {
	payload, _ := marshal(topic, data)
	p.log.Info("event (no-op publisher)", zap.ByteString("envelope", payload))
	return nil
}

func (p *noopPublisher) Close() {}
