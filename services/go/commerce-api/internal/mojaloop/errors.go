package mojaloop

import (
	"encoding/json"
	"strconv"
	"strings"
)

// ErrorKind classifies Mojaloop failures so commerce-api can persist a
// truthful payment status and decide on operator alerting vs. client retry.
type ErrorKind string

const (
	KindValidation       ErrorKind = "validation"         // client-side input problem
	KindPayeeNotFound    ErrorKind = "payee_not_found"    // party lookup failed
	KindQuoteRejected    ErrorKind = "quote_rejected"     // quote refused / inconsistent
	KindTransferRejected ErrorKind = "transfer_rejected"  // transfer refused / aborted / bad fulfilment
	KindDuplicate        ErrorKind = "duplicate"          // idempotent replay of an existing object
	KindTimeout          ErrorKind = "timeout"            // attempt/flow deadline exceeded
	KindUnavailable      ErrorKind = "switch_unavailable" // transport or 5xx from the switch
)

// Error is a classified Mojaloop failure.
type Error struct {
	Op         string    `json:"op"`
	Kind       ErrorKind `json:"kind"`
	HTTPStatus int       `json:"http_status,omitempty"`
	// FSPIOPCode is the numeric FSPIOP error code from an errorInformation
	// body when the switch returned one (e.g. "3201" payee not found).
	FSPIOPCode string `json:"fspiop_code,omitempty"`
	Message    string `json:"message"`
}

func (e *Error) Error() string {
	if e.HTTPStatus != 0 {
		return "mojaloop " + e.Op + ": " + string(e.Kind) + " (http " + strconv.Itoa(e.HTTPStatus) + "): " + e.Message
	}
	return "mojaloop " + e.Op + ": " + string(e.Kind) + ": " + e.Message
}

// PaymentStatus maps a Mojaloop failure to a commerce.fare_payments status.
// Success is "settled" and is set by the handler; these are the failure
// states. The "mojaloop_" prefix keeps them distinct from ledger failures.
func PaymentStatus(err error) string {
	if err == nil {
		return "settled"
	}
	kind := KindUnavailable
	if e, ok := err.(*Error); ok {
		kind = e.Kind
	}
	switch kind {
	case KindPayeeNotFound:
		return "mojaloop_payee_not_found"
	case KindQuoteRejected:
		return "mojaloop_quote_rejected"
	case KindTransferRejected:
		return "mojaloop_transfer_rejected"
	case KindTimeout:
		return "mojaloop_timeout"
	case KindValidation:
		return "failed"
	default:
		return "mojaloop_unavailable"
	}
}

// fspiopErrorInformation is the FSPIOP error body shape.
type fspiopErrorInformation struct {
	ErrorInformation struct {
		ErrorCode        string `json:"errorCode"`
		ErrorDescription string `json:"errorDescription"`
	} `json:"errorInformation"`
}

// classify maps an HTTP status + optional FSPIOP error body onto an ErrorKind.
// FSPIOP error codes (Mojaloop API spec): 3xxx = client errors,
// 32xx communication/party, 33xx quote... We use the documented ones:
//
//	3201 destination FSP error / payee not found family
//	3204 party with the provided identifier not found
//	3208 duplicate transfer id
//	4001/4102 payer/quote rejections → 4xx handled by status class anyway
func classify(method, path string, status int, body []byte) ErrorKind {
	code := fspiopCode(body)
	if code == "3208" {
		return KindDuplicate
	}
	switch {
	case strings.HasPrefix(path, "/parties"):
		if status == 404 || code == "3204" || code == "3201" {
			return KindPayeeNotFound
		}
		return KindPayeeNotFound // any parties failure blocks the flow at discovery
	case strings.HasPrefix(path, "/quotes"):
		if status >= 500 {
			return KindUnavailable
		}
		return KindQuoteRejected
	case strings.HasPrefix(path, "/transfers"):
		if status >= 500 {
			return KindUnavailable
		}
		return KindTransferRejected
	}
	if status >= 500 {
		return KindUnavailable
	}
	return KindTransferRejected
}

func fspiopCode(body []byte) string {
	var info fspiopErrorInformation
	if err := json.Unmarshal(body, &info); err != nil {
		return ""
	}
	return info.ErrorInformation.ErrorCode
}
