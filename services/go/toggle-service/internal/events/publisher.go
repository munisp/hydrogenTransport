// Package events publishes CloudEvents-ish JSON envelopes to Kafka (SPEC §3.3).
package events

import (
	"context"
	"encoding/json"
	"strings"
	"time"

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

// Publisher publishes domain events to the Kafka backbone.
type Publisher interface {
	Publish(ctx context.Context, topic string, data any) error
	Close()
}

// NewPublisher returns a Kafka publisher seeded from KAFKA_BROKERS
// (comma-separated). When empty, a logging no-op publisher is returned so the
// service still runs in minimal dev environments.
func NewPublisher(brokers, source string, log *zap.Logger) Publisher {
	if strings.TrimSpace(brokers) == "" {
		log.Warn("KAFKA_BROKERS not set; events will be logged but not published")
		return &noopPublisher{log: log, source: source}
	}
	client, err := kgo.NewClient(
		kgo.SeedBrokers(strings.Split(brokers, ",")...),
		kgo.DefaultProduceTopic(""), // topic set per-record
		kgo.RequiredAcks(kgo.AllISRAcks()),
	)
	if err != nil {
		log.Error("kafka client init failed; falling back to no-op publisher", zap.Error(err))
		return &noopPublisher{log: log, source: source}
	}
	return &kafkaPublisher{client: client, source: source, log: log}
}

type kafkaPublisher struct {
	client *kgo.Client
	source string
	log    *zap.Logger
}

func (p *kafkaPublisher) Publish(ctx context.Context, topic string, data any) error {
	payload, err := json.Marshal(Envelope{
		ID:     uuid.NewString(),
		Type:   topic,
		Source: p.source,
		Time:   time.Now().UTC().Format(time.RFC3339),
		Data:   data,
	})
	if err != nil {
		return err
	}
	rec := &kgo.Record{Topic: topic, Value: payload}
	if err := p.client.ProduceSync(ctx, rec).FirstErr(); err != nil {
		p.log.Error("kafka produce failed", zap.String("topic", topic), zap.Error(err))
		return err
	}
	p.log.Debug("event published", zap.String("topic", topic), zap.ByteString("payload", payload))
	return nil
}

func (p *kafkaPublisher) Close() { p.client.Close() }

type noopPublisher struct {
	log    *zap.Logger
	source string
}

func (p *noopPublisher) Publish(_ context.Context, topic string, data any) error {
	payload, _ := json.Marshal(Envelope{
		ID:     uuid.NewString(),
		Type:   topic,
		Source: p.source,
		Time:   time.Now().UTC().Format(time.RFC3339),
		Data:   data,
	})
	p.log.Info("event (no-op publisher)", zap.ByteString("envelope", payload))
	return nil
}

func (p *noopPublisher) Close() {}
