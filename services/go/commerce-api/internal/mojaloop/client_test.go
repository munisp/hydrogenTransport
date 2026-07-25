package mojaloop

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// mockSchemeAdapter is a test double for the FSPIOP scheme-adapter/simulator.
// It implements the three endpoints of the payer flow with configurable
// failure injection.
type mockSchemeAdapter struct {
	t *testing.T

	partyFSPID      string
	payeeName       string
	quoteILP        bool // include ilpPacket+condition in quote responses
	failParties     int  // first N parties calls fail with 500
	failTransfers   int  // first N transfers calls fail with 500
	seenTransferIDs map[string]int
}

func (m *mockSchemeAdapter) handler() http.Handler {
	m.seenTransferIDs = map[string]int{}
	mux := http.NewServeMux()
	mux.HandleFunc("/parties/", func(w http.ResponseWriter, r *http.Request) {
		if m.failParties > 0 {
			m.failParties--
			http.Error(w, `{"errorInformation":{"errorCode":"2001","errorDescription":"internal error"}}`, http.StatusInternalServerError)
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/BUSINESS/h2fleet-operator") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"errorInformation":{"errorCode":"3204","errorDescription":"party not found"}}`)
			return
		}
		json.NewEncoder(w).Encode(partiesResponse{Party: party{
			PartyIDInfo: partyIDInfo{PartyIDType: "BUSINESS", PartyID: "h2fleet-operator", FSPID: m.partyFSPID},
			Name:        m.payeeName,
		}})
	})
	mux.HandleFunc("/quotes", func(w http.ResponseWriter, r *http.Request) {
		var q quoteRequest
		_ = json.NewDecoder(r.Body).Decode(&q)
		resp := quoteResponse{
			TransferAmount: q.Amount,
			PayeeFSP:       m.partyFSPID,
			PayerFSP:       "h2fleet",
			Expiration:     time.Now().Add(time.Minute).UTC().Format(time.RFC3339),
		}
		if m.quoteILP {
			// Payee-side ILP generation: condition over a fulfilment the
			// transfers endpoint will return later.
			resp.ILPPacket, resp.Condition, _ = buildILP(q.TransactionID, "payee-secret", 0, time.Now().Add(time.Minute))
		}
		json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("/transfers", func(w http.ResponseWriter, r *http.Request) {
		if m.failTransfers > 0 {
			m.failTransfers--
			http.Error(w, `{"errorInformation":{"errorCode":"2001","errorDescription":"boom"}}`, http.StatusInternalServerError)
			return
		}
		var t transferRequest
		_ = json.NewDecoder(r.Body).Decode(&t)
		m.seenTransferIDs[t.TransferID]++
		if m.seenTransferIDs[t.TransferID] > 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			fmt.Fprint(w, `{"errorInformation":{"errorCode":"3208","errorDescription":"duplicate transferId"}}`)
			return
		}
		resp := TransferResponse{TransferID: t.TransferID, TransferState: TransferStateCommitted}
		if m.quoteILP {
			// Return the fulfilment satisfying the condition we issued in the
			// quote (test-only: derived from the transfer id the same way).
			fulfilment := deriveFulfilment(t.TransferID, "payee-secret")
			resp.Fulfilment = base64.RawURLEncoding.EncodeToString(fulfilment[:])
		}
		// When the payer generated its own ILP (local fallback), the fulfilment
		// is payer-derived and verified client-side; the simulator need not
		// echo anything.
		json.NewEncoder(w).Encode(resp)
	})
	return mux
}

func newTestClient(t *testing.T, endpoint string, mutate func(*Config)) *Client {
	t.Helper()
	cfg := Config{
		Endpoint:       endpoint,
		Secret:         "test-secret",
		AttemptTimeout: 500 * time.Millisecond,
		RetryBudget:    3 * time.Second,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func validRequest() PaymentRequest {
	return PaymentRequest{
		IdempotencyKey: "pay-0001",
		PayerPartyID:   "rider-42",
		AmountMinor:    250,
		Currency:       "EUR",
	}
}

func TestTransferHappyPathPayeeILP(t *testing.T) {
	mock := &mockSchemeAdapter{partyFSPID: "payeefsp", payeeName: "H2Fleet Operator", quoteILP: true}
	srv := httptest.NewServer(mock.handler())
	defer srv.Close()
	c := newTestClient(t, srv.URL, nil)

	res, err := c.Transfer(context.Background(), validRequest())
	if err != nil {
		t.Fatalf("Transfer: %v", err)
	}
	if res.TransferState != TransferStateCommitted {
		t.Fatalf("state = %q, want COMMITTED", res.TransferState)
	}
	if res.PayeeFSPID != "payeefsp" {
		t.Fatalf("payee fsp = %q", res.PayeeFSPID)
	}
	if !res.FulfilmentVerified {
		t.Fatal("expected fulfilment to be returned and verified")
	}
}

func TestTransferLocalILPFallback(t *testing.T) {
	// Quote response without ilpPacket/condition: client must generate them.
	mock := &mockSchemeAdapter{partyFSPID: "payeefsp", quoteILP: false}
	srv := httptest.NewServer(mock.handler())
	defer srv.Close()
	c := newTestClient(t, srv.URL, nil)

	res, err := c.Transfer(context.Background(), validRequest())
	if err != nil {
		t.Fatalf("Transfer: %v", err)
	}
	if res.TransferID == "" {
		t.Fatal("empty transfer id")
	}
}

func TestTransferDeterministicIDs(t *testing.T) {
	mock := &mockSchemeAdapter{partyFSPID: "payeefsp", quoteILP: true}
	srv := httptest.NewServer(mock.handler())
	defer srv.Close()
	c := newTestClient(t, srv.URL, nil)

	r1, err := c.Transfer(context.Background(), validRequest())
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	r2, err := c.Transfer(context.Background(), validRequest()) // replay: switch 409s
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if r1.TransferID != r2.TransferID || r1.QuoteID != r2.QuoteID {
		t.Fatalf("ids not deterministic: %q vs %q", r1.TransferID, r2.TransferID)
	}
	if len(mock.seenTransferIDs) != 1 {
		t.Fatalf("switch saw %d distinct transfer ids, want 1", len(mock.seenTransferIDs))
	}
}

func TestTransferRetryOnTransient5xx(t *testing.T) {
	mock := &mockSchemeAdapter{partyFSPID: "payeefsp", quoteILP: true, failTransfers: 2}
	srv := httptest.NewServer(mock.handler())
	defer srv.Close()
	c := newTestClient(t, srv.URL, nil)

	if _, err := c.Transfer(context.Background(), validRequest()); err != nil {
		t.Fatalf("Transfer with 2 transient failures: %v", err)
	}
}

func TestTransferGivesUpWhenBudgetExhausted(t *testing.T) {
	mock := &mockSchemeAdapter{partyFSPID: "payeefsp", quoteILP: true, failParties: 100}
	srv := httptest.NewServer(mock.handler())
	defer srv.Close()
	c := newTestClient(t, srv.URL, func(cfg *Config) { cfg.RetryBudget = 700 * time.Millisecond })

	start := time.Now()
	_, err := c.Transfer(context.Background(), validRequest())
	if err == nil {
		t.Fatal("expected error when switch is persistently down")
	}
	var mlErr *Error
	if !errors.As(err, &mlErr) {
		t.Fatalf("error type = %T, want *Error", err)
	}
	if mlErr.Kind != KindUnavailable && mlErr.Kind != KindPayeeNotFound {
		t.Fatalf("kind = %q", mlErr.Kind)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("retry budget not respected: took %v", elapsed)
	}
	if got := PaymentStatus(err); got != "mojaloop_unavailable" && got != "mojaloop_payee_not_found" {
		t.Fatalf("PaymentStatus = %q", got)
	}
}

func TestPayeeNotFoundMapsToStatus(t *testing.T) {
	mock := &mockSchemeAdapter{partyFSPID: "payeefsp"}
	srv := httptest.NewServer(mock.handler())
	defer srv.Close()
	c := newTestClient(t, srv.URL, func(cfg *Config) { cfg.PayeePartyID = "nobody" })

	_, err := c.Transfer(context.Background(), validRequest())
	var mlErr *Error
	if !errors.As(err, &mlErr) || mlErr.Kind != KindPayeeNotFound {
		t.Fatalf("kind = %v, want payee_not_found (err=%v)", mlErr, err)
	}
	if got := PaymentStatus(err); got != "mojaloop_payee_not_found" {
		t.Fatalf("PaymentStatus = %q", got)
	}
}

func TestValidationErrors(t *testing.T) {
	c := newTestClient(t, "http://unused", nil)
	for _, req := range []PaymentRequest{
		{IdempotencyKey: "", PayerPartyID: "r", AmountMinor: 100, Currency: "EUR"},
		{IdempotencyKey: "k", PayerPartyID: "r", AmountMinor: 0, Currency: "EUR"},
		{IdempotencyKey: "k", PayerPartyID: "r", AmountMinor: 100, Currency: ""},
	} {
		if _, err := c.Transfer(context.Background(), req); err == nil {
			t.Fatalf("expected validation error for %+v", req)
		} else if got := PaymentStatus(err); got != "failed" {
			t.Fatalf("PaymentStatus = %q, want failed", got)
		}
	}
}

func TestILPRoundTrip(t *testing.T) {
	transferID := "7f1d9f4e-0d7e-4f8e-9a1b-2c3d4e5f6a7b"
	pktB64, cond, ful := buildILP(transferID, "s3cret", 250, time.Now().Add(time.Minute))
	if !verifyFulfilment(ful, cond) {
		t.Fatal("fulfilment does not satisfy condition")
	}
	pkt, err := base64.RawURLEncoding.DecodeString(pktB64)
	if err != nil {
		t.Fatalf("packet not base64url: %v", err)
	}
	if len(pkt) < 2+len(ilpDestination)+8+19+4+32+4 {
		t.Fatalf("packet too short: %d bytes", len(pkt))
	}
	if pkt[0] != ilpTypePrepare {
		t.Fatalf("type byte = %d, want %d", pkt[0], ilpTypePrepare)
	}
	// Deterministic: same inputs → same outputs.
	pkt2, cond2, ful2 := buildILP(transferID, "s3cret", 250, time.Now().Add(time.Minute))
	_ = pkt2
	if cond != cond2 || ful != ful2 {
		t.Fatal("ILP derivation not deterministic")
	}
	if _, cond3, _ := buildILP("other-transfer", "s3cret", 250, time.Now()); cond3 == cond {
		t.Fatal("distinct transfer ids must not collide on condition")
	}
}

func TestMinorToMajor(t *testing.T) {
	for in, want := range map[int64]string{250: "2.50", 100: "1.00", 99: "0.99", 123456: "1234.56"} {
		if got := minorToMajor(in); got != want {
			t.Fatalf("minorToMajor(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestDeterministicIDShape(t *testing.T) {
	id := deterministicID("transfer", "key-1")
	if len(id) != 36 || id[8] != '-' || id[14] != '4' {
		t.Fatalf("id %q not UUID-shaped v4", id)
	}
	if deterministicID("transfer", "key-1") != id {
		t.Fatal("not deterministic")
	}
	if deterministicID("quote", "key-1") == id {
		t.Fatal("namespaces must differ")
	}
}
