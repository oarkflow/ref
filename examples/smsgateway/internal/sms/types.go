// Package sms is the SMS sending application built on REF, oarkflow/smppflow
// and oarkflow/broker.
//
// The application is a set of REF actions (sms.validate_user, sms.check_data,
// sms.route, sms.accept, sms.enqueue, sms.load_job, sms.provider_send,
// sms.settle, ...) that a BCL file composes into pipelines, plus the resource
// kinds those actions run against: sms.hub (state, routing, ledger, consumers),
// sms.gateway.<plugin> (one per provider) and sms.auth (API key
// authentication). The BCL is the application; this package is its vocabulary.
package sms

import (
	"errors"
	"time"
)

// MessageState is the lifecycle of one message.
//
//	queued ──claim──▶ dispatching ──accepted──▶ submitted ──DLR──▶ delivered
//	   ▲                   │                        │
//	   └──retry/failover───┘                        └──DLR──▶ failed
//	                       └──permanent/exhausted──▶ failed
type MessageState string

const (
	// StateQueued: accepted and paid for, waiting for a provider job to run.
	StateQueued MessageState = "queued"
	// StateDispatching: a worker holds the claim and is submitting.
	StateDispatching MessageState = "dispatching"
	// StateSubmitted: a provider accepted the message.
	StateSubmitted MessageState = "submitted"
	// StateDelivered: the handset received it (DLR).
	StateDelivered MessageState = "delivered"
	// StateFailed: terminal failure; the charge was released or refunded.
	StateFailed MessageState = "failed"
)

// Final reports whether a message can no longer change by dispatching.
func (s MessageState) Final() bool { return s == StateDelivered || s == StateFailed }

// Settled reports whether dispatching is over, for a message that was handed to
// a provider.
func (s MessageState) Settled() bool {
	return s == StateSubmitted || s == StateDelivered || s == StateFailed
}

// Message types. They drive routing (an OTP wants quality, a promotion wants
// price) and are free-form beyond these three.
const (
	TypeOTP           = "otp"
	TypeTransactional = "transactional"
	TypePromotional   = "promotional"
)

// PlanEntry is one provider in a message's ordered fallback chain.
type PlanEntry struct {
	Provider string  `json:"provider"`
	Rule     string  `json:"rule"`
	Tier     string  `json:"tier"` // user, tenant, country, platform
	Priority int     `json:"priority"`
	Score    float64 `json:"score"`
	// CostMicros is the provider's cost for the whole message.
	CostMicros int64 `json:"cost_micros"`
}

// Message is the system of record for one SMS.
type Message struct {
	ID             string       `json:"id"`
	UserID         string       `json:"user_id"`
	Tenant         string       `json:"tenant"`
	IdempotencyKey string       `json:"idempotency_key,omitempty"`
	PayloadHash    string       `json:"-"`
	From           string       `json:"from"`
	To             string       `json:"to"`
	Country        string       `json:"country"`
	Text           string       `json:"text,omitempty"`
	Encoding       string       `json:"encoding"`
	Segments       int          `json:"segments"`
	Type           string       `json:"type"`
	WantDLR        bool         `json:"dlr"`
	State          MessageState `json:"state"`

	Plan          []PlanEntry `json:"plan"`
	PlanIndex     int         `json:"plan_index"`
	Provider      string      `json:"provider,omitempty"`
	ProviderMsgID string      `json:"provider_message_id,omitempty"`
	// Attempt counts attempts on the current provider; TotalAttempts across all.
	Attempt       int `json:"attempt"`
	TotalAttempts int `json:"total_attempts"`
	// DispatchSeq numbers the live job. A job whose sequence is not the
	// message's is a stale duplicate and is dropped.
	DispatchSeq int64 `json:"-"`

	PriceMicros int64  `json:"price_micros"`
	CostMicros  int64  `json:"cost_micros"`
	Currency    string `json:"currency"`

	ErrorCode string `json:"error_code,omitempty"`
	Error     string `json:"error,omitempty"`

	ExpiresAt   time.Time `json:"expires_at,omitzero"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	SubmittedAt time.Time `json:"submitted_at,omitzero"`
	DeliveredAt time.Time `json:"delivered_at,omitzero"`
	NextRunAt   time.Time `json:"-"`
	ClaimUntil  time.Time `json:"-"`

	Meta map[string]string `json:"meta,omitempty"`
}

// Attempt is one try at one provider, kept for audit and tuning.
type AttemptRecord struct {
	MessageID string    `json:"message_id"`
	N         int       `json:"n"`
	Provider  string    `json:"provider"`
	Outcome   string    `json:"outcome"` // accepted, retry, failover, failed
	Code      string    `json:"code,omitempty"`
	Error     string    `json:"error,omitempty"`
	LatencyMs int64     `json:"latency_ms"`
	At        time.Time `json:"at"`
}

// Money is held in micros of the user's currency: 1 unit = 1,000,000 micros.
const Micros = 1_000_000

// FromUnits converts a decimal amount to micros, rounding to the nearest micro.
func FromUnits(v float64) int64 {
	if v < 0 {
		return -int64(-v*Micros + 0.5)
	}
	return int64(v*Micros + 0.5)
}

// ToUnits converts micros to a decimal amount.
func ToUnits(m int64) float64 { return float64(m) / Micros }

// Domain errors. The pipeline maps them to REF failures with HTTP statuses.
var (
	ErrNotFound          = errors.New("not found")
	ErrInsufficientFunds = errors.New("insufficient funds")
	ErrIdempotency       = errors.New("idempotency key reused with a different payload")
	ErrStale             = errors.New("stale dispatch")
	ErrUnknownUser       = errors.New("unknown user")
)

// UserStatus values.
const (
	UserActive    = "active"
	UserSuspended = "suspended"
)

// User is an account that sends messages and pays for them.
type User struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Tenant   string `json:"tenant,omitempty"` // organisation; defaults to the user id
	Status   string `json:"status"`
	Currency string `json:"currency"`
	// APIKeyHash is the SHA-256 of the user's API key.
	APIKeyHash string `json:"api_key_hash,omitempty"`
	// Senders are the sender ids the user may send from. Empty allows any.
	Senders       []string `json:"senders,omitempty"`
	DefaultSender string   `json:"default_sender,omitempty"`
	// Countries restricts destinations (ISO alpha-2). Empty allows all.
	Countries []string `json:"countries,omitempty"`
	// RatePerSecond caps submissions per second; 0 is unlimited.
	RatePerSecond int `json:"rate_per_second,omitempty"`
	// DailyLimit caps accepted messages per UTC day; 0 is unlimited.
	DailyLimit int `json:"daily_limit,omitempty"`
	// MaxCost caps the price of one message in currency units; 0 is unlimited.
	MaxPrice float64 `json:"max_price,omitempty"`
	// Objective is the routing objective the user prefers: balanced,
	// lowest_cost or highest_delivery. Empty uses the platform's.
	Objective string            `json:"objective,omitempty"`
	Meta      map[string]string `json:"meta,omitempty"`
	CreatedAt time.Time         `json:"created_at,omitzero"`
}

// Assignment makes a provider available to a user (or a whole tenant),
// optionally limited to countries and message types.
type Assignment struct {
	ID       string `json:"id"`
	UserID   string `json:"user_id,omitempty"`
	Tenant   string `json:"tenant,omitempty"`
	Provider string `json:"provider"`
	// Countries limits the assignment; empty means every country the provider
	// serves.
	Countries []string `json:"countries,omitempty"`
	// Prefixes narrow the assignment to recipient number ranges (E.164 digits),
	// replacing the countries' dial codes.
	Prefixes []string `json:"prefixes,omitempty"`
	// Senders limits the assignment to messages sent from these sender ids.
	Senders      []string `json:"senders,omitempty"`
	MessageTypes []string `json:"message_types,omitempty"`
	// Priority orders assignments: lower wins. Zero picks the default for the
	// assignment's scope (10 for a user and country, 20 for a user, 30 for a
	// tenant).
	Priority int `json:"priority,omitempty"`
	Weight   int `json:"weight,omitempty"`
	// Exclusive stops the route from falling back to platform providers: the
	// user brings their own provider and wants no other path used.
	Exclusive bool `json:"exclusive,omitempty"`
	Disabled  bool `json:"disabled,omitempty"`
}

// Rate is a sell price per segment. The most specific rate wins: user and
// country, user, country, then the platform default.
type Rate struct {
	ID                   string `json:"id"`
	UserID               string `json:"user_id,omitempty"`
	Country              string `json:"country,omitempty"`
	MessageType          string `json:"message_type,omitempty"`
	SellPerSegmentMicros int64  `json:"sell_per_segment_micros"`
	Currency             string `json:"currency,omitempty"`
}
