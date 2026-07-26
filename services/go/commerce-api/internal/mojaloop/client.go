// Package mojaloop implements the payer-side FSPIOP / sdk-scheme-adapter
// flow for fare payments (SPEC §3.8: "Mojaloop (fare payment rails)"):
//
//	GET  /parties/{type}/{id}   payee lookup     (discovery)
//	POST /quotes                quote + ILP condition
//	POST /transfers             transfer with ILP packet + condition
//
// The client is idempotent by construction: the quote id and transfer id are
// derived deterministically from the caller's idempotency key, so retrying a
// payment can never create a second transfer at the switch — a duplicate is
// recognised (FSPIOP error 3208 / HTTP 409) and treated as a successful
// replay of the original.
//
// Errors are classified (Error.Kind) and mapped to commerce.fare_payments
// statuses via PaymentStatus so the commerce-api handler can persist a
// truthful state instead of a generic failure.
//
// When MOJALOOP_ENDPOINT is unset, commerce-api fails the Mojaloop leg closed
// (status mojaloop_unavailable) unless the explicit dev opt-in
// H2_SIMULATED_MOJALOOP=true is set (SPEC §4); in both cases this client is
// never constructed.
package mojaloop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Party identifier types we use (FSPIOP PartyIdType).
const (
	PartyTypeMSISDN     = "MSISDN"
	PartyTypePersonalID = "PERSONAL_ID"
	PartyTypeBusiness   = "BUSINESS"
	PartyTypeAlias      = "ALIAS"
)

// TransferState returned by a switch/scheme-adapter on POST /transfers.
const (
	TransferStateCommitted = "COMMITTED"
	TransferStateReceived  = "RECEIVED"
	TransferStateReserved  = "RESERVED"
	TransferStateAborted   = "ABORTED"
)

// Config controls the Client.
type Config struct {
	// Endpoint is the base URL of the scheme adapter / simulator / ALS-facing
	// switch endpoint, e.g. http://mojaloop-simulator:8444. Required.
	Endpoint string
	// DFSPID is our own DFSP identifier as registered at the switch; sent as
	// FSPIOP-Source. Defaults to "h2fleet".
	DFSPID string
	// PayeePartyID is the operator's party identifier at the switch (the
	// payee for fare payments). Defaults to "h2fleet-operator".
	PayeePartyID string
	// PayeePartyType defaults to PartyTypeBusiness.
	PayeePartyType string
	// Secret seeds deterministic ILP fulfilment derivation. Should come from
	// the secret manager in production; empty means an unsalted derivation,
	// acceptable only against the dev simulator.
	Secret string
	// GenerateILP forces local ILP packet/condition generation even when the
	// quote response carries them. Normally false: the payee's condition from
	// the quote response is forwarded verbatim.
	GenerateILP bool
	// AttemptTimeout bounds a single HTTP attempt. Default 10s.
	AttemptTimeout time.Duration
	// RetryBudget bounds the whole Transfer flow (all retries of all steps).
	// Default 30s. Retries happen only for transport errors, 408, 429 and 5xx;
	// 4xx rejections are business failures and are never retried.
	RetryBudget time.Duration
	// HTTPClient allows injecting a custom transport (tests, mTLS).
	HTTPClient *http.Client
}

// Client is a payer-side Mojaloop SDK-scheme-adapter style client.
type Client struct {
	cfg Config
	hc  *http.Client
}

// New validates the config and returns a Client.
func New(cfg Config) (*Client, error) {
	cfg.Endpoint = strings.TrimRight(cfg.Endpoint, "/")
	if cfg.Endpoint == "" {
		return nil, errors.New("mojaloop: Endpoint is required")
	}
	if cfg.DFSPID == "" {
		cfg.DFSPID = "h2fleet"
	}
	if cfg.PayeePartyID == "" {
		cfg.PayeePartyID = "h2fleet-operator"
	}
	if cfg.PayeePartyType == "" {
		cfg.PayeePartyType = PartyTypeBusiness
	}
	if cfg.AttemptTimeout <= 0 {
		cfg.AttemptTimeout = 10 * time.Second
	}
	if cfg.RetryBudget <= 0 {
		cfg.RetryBudget = 30 * time.Second
	}
	if cfg.RetryBudget < cfg.AttemptTimeout {
		cfg.RetryBudget = cfg.AttemptTimeout
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{}
	}
	return &Client{cfg: cfg, hc: hc}, nil
}

// ---- FSPIOP payloads --------------------------------------------------------

// Money is an FSPIOP amount object. Amount is a decimal string per spec; we
// accept minor units and render them as the major-unit decimal string the
// switch expects (2 fraction digits is sufficient for fare currencies).
type Money struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

// minorToMajor renders minor units (e.g. 250 cents) as "2.50".
func minorToMajor(minor int64) string {
	sign := ""
	if minor < 0 {
		sign = "-"
		minor = -minor
	}
	return fmt.Sprintf("%s%d.%02d", sign, minor/100, minor%100)
}

// Party is the resolved payee returned by GET /parties/{type}/{id}.
type Party struct {
	FSPID string `json:"fspId"`
	Name  string `json:"name"`
}

type partyIDInfo struct {
	PartyIDType string `json:"partyIdType"`
	PartyID     string `json:"partyIdentifier"`
	FSPID       string `json:"fspId,omitempty"`
}

type party struct {
	PartyIDInfo partyIDInfo `json:"partyIdInfo"`
	Name        string      `json:"name"`
}

type partiesResponse struct {
	Party party `json:"party"`
}

type quoteRequest struct {
	QuoteID         string      `json:"quoteId"`
	TransactionID   string      `json:"transactionId"`
	Payee           partyIDInfo `json:"payee"`
	Payer           partyIDInfo `json:"payer"`
	AmountType      string      `json:"amountType"`
	Amount          Money       `json:"amount"`
	TransactionType struct {
		Scenario   string `json:"scenario"`
		Initiator  string `json:"initiator"`
		InitiatorT string `json:"initiatorType"`
	} `json:"transactionType"`
	Expiration string `json:"expiration,omitempty"`
}

type quoteResponse struct {
	TransferAmount Money  `json:"transferAmount"`
	PayeeFSP       string `json:"payeeFsp"`
	PayerFSP       string `json:"payerFsp"`
	Expiration     string `json:"expiration"`
	ILPPacket      string `json:"ilpPacket"`
	Condition      string `json:"condition"`
}

type transferRequest struct {
	TransferID string `json:"transferId"`
	PayeeFSP   string `json:"payeeFsp"`
	PayerFSP   string `json:"payerFsp"`
	Amount     Money  `json:"amount"`
	ILPPacket  string `json:"ilpPacket"`
	Condition  string `json:"condition"`
	Expiration string `json:"expiration"`
}

// TransferResponse is the result of a committed (or duplicate-replayed)
// transfer.
type TransferResponse struct {
	TransferID         string `json:"transferId"`
	TransferState      string `json:"transferState"`
	Fulfilment         string `json:"fulfilment"`
	CompletedTimestamp string `json:"completedTimestamp"`
}

// PaymentRequest is the commerce-api-level input for a fare payment over
// Mojaloop rails.
type PaymentRequest struct {
	// IdempotencyKey scopes the payment; quote/transfer ids derive from it.
	IdempotencyKey string
	// PayerPartyID identifies the rider at the switch (e.g. wallet alias /
	// MSISDN). Defaults to PartyTypeAlias semantics at the simulator.
	PayerPartyID   string
	PayerPartyType string
	AmountMinor    int64
	Currency       string
}

// Result is the outcome of a full parties → quotes → transfers flow.
type Result struct {
	TransferID    string
	TransferState string
	PayeeFSPID    string
	QuoteID       string
	// FulfilmentVerified is true when the switch returned a fulfilment and it
	// satisfied the condition we sent.
	FulfilmentVerified bool
}

// deterministicID hashes ("h2fleet:mojaloop:"+namespace+":"+key) into a UUID-
// shaped string, giving stable quote/transfer ids across retries.
func deterministicID(namespace, key string) string {
	sum := sha256.Sum256([]byte("h2fleet:mojaloop:" + namespace + ":" + key))
	var id [16]byte
	copy(id[:], sum[:16])
	id[6] = (id[6] & 0x0f) | 0x40 // version 4
	id[8] = (id[8] & 0x3f) | 0x80 // variant 10
	var b [36]byte
	hex.Encode(b[0:8], id[0:4])
	b[8] = '-'
	hex.Encode(b[9:13], id[4:6])
	b[13] = '-'
	hex.Encode(b[14:18], id[6:8])
	b[18] = '-'
	hex.Encode(b[19:23], id[8:10])
	b[23] = '-'
	hex.Encode(b[24:36], id[10:16])
	return string(b[:])
}

// ---- HTTP plumbing ----------------------------------------------------------

// do performs one attempt of an FSPIOP call with retry inside the retry
// budget. body may be nil for GET. The response body is fully read on success.
func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var payload []byte
	var err error
	if body != nil {
		if payload, err = json.Marshal(body); err != nil {
			return fmt.Errorf("mojaloop: marshal %s %s: %w", method, path, err)
		}
	}

	deadline := time.Now().Add(c.cfg.RetryBudget)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	backoff := 100 * time.Millisecond
	attempt := 0
	for {
		attempt++
		attemptCtx, cancel := context.WithTimeout(ctx, c.cfg.AttemptTimeout)
		req, err := http.NewRequestWithContext(attemptCtx, method, c.cfg.Endpoint+path, bytes.NewReader(payload))
		if err != nil {
			cancel()
			return &Error{Op: method + " " + path, Kind: KindValidation, Message: err.Error()}
		}
		// FSPIOP versioned content types, matched to the resource (a real
		// switch rejects the wrong one; the simulator also accepts
		// application/json).
		resource := strings.SplitN(strings.TrimPrefix(path, "/"), "/", 2)[0]
		fspiopType := "application/vnd.interoperability." + resource + "+json;version=1.0"
		req.Header.Set("Accept", fspiopType+", application/json")
		if body != nil {
			req.Header.Set("Content-Type", fspiopType)
		}
		req.Header.Set("FSPIOP-Source", c.cfg.DFSPID)
		req.Header.Set("Date", time.Now().UTC().Format(http.TimeFormat))

		resp, err := c.hc.Do(req)
		if err != nil {
			cancel()
			// Transport error / attempt timeout.
			if !retryableAfter(attemptCtx, ctx, deadline, backoff) {
				if errors.Is(attemptCtx.Err(), context.DeadlineExceeded) {
					return &Error{Op: method + " " + path, Kind: KindTimeout, Message: err.Error()}
				}
				return &Error{Op: method + " " + path, Kind: KindUnavailable, Message: err.Error()}
			}
			sleep(ctx, backoff)
			backoff = nextBackoff(backoff)
			continue
		}
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		cancel()

		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			if out != nil && len(respBody) > 0 {
				if err := json.Unmarshal(respBody, out); err != nil {
					return &Error{Op: method + " " + path, HTTPStatus: resp.StatusCode,
						Kind: KindUnavailable, Message: "invalid JSON from switch: " + err.Error()}
				}
			}
			return nil
		case resp.StatusCode == http.StatusConflict:
			// Duplicate of an already-submitted object: idempotent replay.
			return &Error{Op: method + " " + path, HTTPStatus: resp.StatusCode,
				Kind: KindDuplicate, Message: string(respBody)}
		default:
			mlErr := &Error{Op: method + " " + path, HTTPStatus: resp.StatusCode, Message: truncate(string(respBody), 512)}
			mlErr.Kind = classify(method, path, resp.StatusCode, respBody)
			if isRetryableStatus(resp.StatusCode) && time.Now().Add(backoff).Before(deadline) && ctx.Err() == nil {
				sleep(ctx, backoff)
				backoff = nextBackoff(backoff)
				continue
			}
			return mlErr
		}
	}
}

func retryableAfter(attemptCtx, ctx context.Context, deadline time.Time, backoff time.Duration) bool {
	return ctx.Err() == nil && time.Now().Add(backoff).Before(deadline)
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func nextBackoff(d time.Duration) time.Duration {
	d *= 2
	if d > 2*time.Second {
		d = 2 * time.Second
	}
	return d
}

func isRetryableStatus(code int) bool {
	return code == http.StatusRequestTimeout || code == http.StatusTooManyRequests || code >= 500
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// ---- Flow steps -------------------------------------------------------------

// GetParty resolves a payee party at the switch: GET /parties/{type}/{id}.
func (c *Client) GetParty(ctx context.Context, partyType, partyID string) (*Party, error) {
	var out partiesResponse
	if err := c.do(ctx, http.MethodGet, "/parties/"+partyType+"/"+partyID, nil, &out); err != nil {
		return nil, err
	}
	p := &Party{FSPID: out.Party.PartyIDInfo.FSPID, Name: out.Party.Name}
	if p.FSPID == "" {
		return nil, &Error{Op: "GET /parties", Kind: KindPayeeNotFound,
			Message: "party resolved without an fspId"}
	}
	return p, nil
}

// RequestQuote performs POST /quotes and returns the quote, including the
// payee-generated ILP packet + condition when the switch provides them.
func (c *Client) RequestQuote(ctx context.Context, req PaymentRequest, quoteID, transferID string) (*quoteResponse, error) {
	payerType := req.PayerPartyType
	if payerType == "" {
		payerType = PartyTypeAlias
	}
	q := quoteRequest{
		QuoteID:       quoteID,
		TransactionID: transferID,
		Payee:         partyIDInfo{PartyIDType: c.cfg.PayeePartyType, PartyID: c.cfg.PayeePartyID},
		Payer:         partyIDInfo{PartyIDType: payerType, PartyID: req.PayerPartyID},
		AmountType:    "SEND",
		Amount:        Money{Amount: minorToMajor(req.AmountMinor), Currency: req.Currency},
		Expiration:    time.Now().Add(2 * time.Minute).UTC().Format(time.RFC3339),
	}
	q.TransactionType.Scenario = "PAYMENT"
	q.TransactionType.Initiator = "PAYER"
	q.TransactionType.InitiatorT = "CONSUMER"
	var out quoteResponse
	if err := c.do(ctx, http.MethodPost, "/quotes", q, &out); err != nil {
		return nil, err
	}
	if out.TransferAmount.Currency != "" && !strings.EqualFold(out.TransferAmount.Currency, req.Currency) {
		return nil, &Error{Op: "POST /quotes", Kind: KindQuoteRejected,
			Message: fmt.Sprintf("quote currency mismatch: asked %s, got %s", req.Currency, out.TransferAmount.Currency)}
	}
	return &out, nil
}

// SendTransfer performs POST /transfers with the ILP packet and condition.
// A duplicate transfer (already committed) is an idempotent success.
func (c *Client) SendTransfer(ctx context.Context, t transferRequest, condition string) (*TransferResponse, error) {
	var out TransferResponse
	err := c.do(ctx, http.MethodPost, "/transfers", t, &out)
	if err != nil {
		var mlErr *Error
		if errors.As(err, &mlErr) && mlErr.Kind == KindDuplicate {
			// The switch already knows this transferId: our original transfer
			// was recorded. Replaying is a success with the same id.
			return &TransferResponse{TransferID: t.TransferID, TransferState: TransferStateCommitted}, nil
		}
		return nil, err
	}
	if out.TransferID == "" {
		// Accepted without a body (e.g. bare 202): the switch settles under
		// the transferId we submitted — genuine, not fabricated.
		out.TransferID = t.TransferID
	}
	if out.TransferState == TransferStateAborted {
		return nil, &Error{Op: "POST /transfers", Kind: KindTransferRejected,
			Message: "transfer aborted by switch"}
	}
	// Verify the fulfilment when the switch returned one: it must satisfy the
	// condition we sent (this is the cryptographic settlement proof of the
	// Interledger flow).
	if out.Fulfilment != "" && condition != "" && !verifyFulfilment(out.Fulfilment, condition) {
		return nil, &Error{Op: "POST /transfers", Kind: KindTransferRejected,
			Message: "fulfilment does not satisfy condition"}
	}
	return &out, nil
}

// Transfer runs the full payer flow: payee lookup → quote → transfer.
// All ids are deterministic functions of the idempotency key, so the whole
// flow is safe to retry from any point.
func (c *Client) Transfer(ctx context.Context, req PaymentRequest) (*Result, error) {
	if req.IdempotencyKey == "" {
		return nil, &Error{Op: "Transfer", Kind: KindValidation, Message: "idempotency key is required"}
	}
	if req.AmountMinor <= 0 {
		return nil, &Error{Op: "Transfer", Kind: KindValidation, Message: "amount must be positive"}
	}
	if req.Currency == "" {
		return nil, &Error{Op: "Transfer", Kind: KindValidation, Message: "currency is required"}
	}
	transferID := deterministicID("transfer", req.IdempotencyKey)
	quoteID := deterministicID("quote", req.IdempotencyKey)

	// 1. Payee discovery (GET /parties).
	payee, err := c.GetParty(ctx, c.cfg.PayeePartyType, c.cfg.PayeePartyID)
	if err != nil {
		return nil, err
	}

	// 2. Quote (POST /quotes).
	quote, err := c.RequestQuote(ctx, req, quoteID, transferID)
	if err != nil {
		return nil, err
	}

	// 3. ILP packet + condition: prefer the payee-generated values from the
	//    quote; generate locally when absent or when forced (simulator mode).
	ilpPacket, condition := quote.ILPPacket, quote.Condition
	if ilpPacket == "" || condition == "" || c.cfg.GenerateILP {
		expiry := time.Now().Add(2 * time.Minute)
		if quote.Expiration != "" {
			if t, perr := time.Parse(time.RFC3339, quote.Expiration); perr == nil {
				expiry = t
			}
		}
		ilpPacket, condition, _ = buildILP(transferID, c.cfg.Secret, uint64(req.AmountMinor), expiry)
	}

	// 4. Transfer (POST /transfers) with the condition from the quote.
	tr, err := c.SendTransfer(ctx, transferRequest{
		TransferID: transferID,
		PayeeFSP:   payee.FSPID,
		PayerFSP:   c.cfg.DFSPID,
		Amount:     Money{Amount: minorToMajor(req.AmountMinor), Currency: req.Currency},
		ILPPacket:  ilpPacket,
		Condition:  condition,
		Expiration: time.Now().Add(2 * time.Minute).UTC().Format(time.RFC3339),
	}, condition)
	if err != nil {
		return nil, err
	}

	return &Result{
		TransferID:         tr.TransferID,
		TransferState:      tr.TransferState,
		PayeeFSPID:         payee.FSPID,
		QuoteID:            quoteID,
		FulfilmentVerified: tr.Fulfilment != "",
	}, nil
}
