// Package consumers closes the commerce-domain event loops
// (BUSINESS_LOGIC_AUDIT: carbon.credit.issued was produced-never-consumed,
// leaving the CARBON_FUND ledger leg of SPEC §3.4 unimplemented).
package consumers

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"go.uber.org/zap"

	"github.com/munisp/hydrogenTransport/services/go/commerce-api/internal/ledger"
)

// CarbonCreditIssued is the data payload of a carbon.credit.issued event
// (services/python/carbon-analytics/app/core.py build_envelope).
type CarbonCreditIssued struct {
	CreditID     string  `json:"credit_id"`
	Period       string  `json:"period"`
	KgCO2Avoided float64 `json:"kg_co2_avoided"`
	Credits      float64 `json:"credits"`
}

type envelope struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// PostCarbonFundLeg posts the CARBON_FUND ledger leg for an issued carbon
// credit: platform issuance source (4002) → carbon fund (4001), amount = kg
// CO2 avoided (whole kg). The transfer id is deterministic per credit id —
// and carbon-analytics reissues the SAME credit id on recompute (UUIDv5 per
// period) — so recomputes and consumer redeliveries are idempotent retries,
// never double-posts.
func PostCarbonFundLeg(c CarbonCreditIssued, led ledger.Ledger) (string, error) {
	if c.CreditID == "" || c.Period == "" {
		return "", errors.New("carbon.credit.issued payload missing credit_id/period")
	}
	amount := uint64(math.Round(math.Max(c.KgCO2Avoided, 0)))
	if amount == 0 {
		// A zero-avoidance period carries no fund movement; skip honestly.
		return "", nil
	}
	return led.Transfer(
		ledger.DeterministicTransferID("carbon:"+c.CreditID),
		ledger.CarbonIssuanceAccount, ledger.CarbonFundAccount,
		amount, ledger.CodeCarbon)
}

// StartCarbonConsumer consumes carbon.credit.issued and posts the
// CARBON_FUND ledger leg for each issuance. Offsets commit only after the
// ledger posting lands. brokers empty is a no-op.
func StartCarbonConsumer(ctx context.Context, brokers string, led ledger.Ledger, log *zap.Logger) {
	if strings.TrimSpace(brokers) == "" {
		log.Warn("KAFKA_BROKERS not set; carbon.credit.issued consumer disabled")
		return
	}
	client, err := kgo.NewClient(
		kgo.SeedBrokers(strings.Split(brokers, ",")...),
		kgo.ConsumerGroup("commerce-api-carbon-fund"),
		kgo.ConsumeTopics("carbon.credit.issued"),
		kgo.DisableAutoCommit(),
	)
	if err != nil {
		log.Error("carbon consumer init failed", zap.Error(err))
		return
	}
	defer client.Close()
	log.Info("carbon.credit.issued consumer started")

	for {
		if ctx.Err() != nil {
			return
		}
		fetches := client.PollFetches(ctx)
		if errs := fetches.Errors(); len(errs) > 0 {
			if ctx.Err() != nil {
				return
			}
			for _, e := range errs {
				log.Error("carbon.credit.issued fetch error", zap.Error(e.Err))
			}
			time.Sleep(2 * time.Second)
			continue
		}
		fetches.EachRecord(func(rec *kgo.Record) {
			var env envelope
			if err := json.Unmarshal(rec.Value, &env); err != nil {
				log.Warn("dropping malformed carbon.credit.issued message", zap.Error(err))
				commit(client, rec, log)
				return
			}
			var c CarbonCreditIssued
			if err := json.Unmarshal(env.Data, &c); err != nil {
				log.Warn("dropping carbon.credit.issued with bad data payload", zap.Error(err))
				commit(client, rec, log)
				return
			}
			if _, err := PostCarbonFundLeg(c, led); err != nil {
				log.Error("carbon fund ledger leg failed", zap.Error(err))
				return // uncommitted → redelivered
			}
			commit(client, rec, log)
		})
	}
}

func commit(client *kgo.Client, rec *kgo.Record, log *zap.Logger) {
	if err := client.CommitRecords(context.Background(), rec); err != nil {
		log.Error("offset commit failed", zap.Error(err))
	}
}
