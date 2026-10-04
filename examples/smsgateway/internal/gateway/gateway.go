// Package gateway is the provider plugin contract of the SMS application.
//
// A Gateway delivers one message through one upstream: an SMPP bind, a vendor
// HTTP API, a test double. Everything the platform needs to route around
// failure is expressed through this package rather than through provider
// specifics:
//
//   - Send either returns a Receipt (the upstream accepted the message) or an
//     *Error that says how to react: retry it, fail it for good, back off.
//   - A gateway that learns about delivery asynchronously (an SMPP deliver_sm
//     receipt, a vendor webhook) reports it through its DLRSink.
//
// Plugins register a Factory under a kind name with Register. The application
// turns every registered kind into a BCL resource kind, sms.gateway.<kind>, so
// adding a provider type is a Go package with one init() and no change to the
// platform:
//
//	func init() { gateway.Register("acme", openAcme) }
package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"
)

// Message is what a gateway is asked to deliver.
type Message struct {
	// ID is the platform's message id. It is stable across retries and
	// failovers, so a gateway that supports client references should send it.
	ID          string
	From        string
	To          string // E.164 digits, no plus sign
	Text        string
	Segments    int
	Encoding    string // gsm7 or ucs2
	MessageType string // otp, transactional, promotional
	Country     string // ISO 3166 alpha-2
	WantDLR     bool
	ExpiresAt   time.Time
	// Attempt is the 1-based attempt number on this provider.
	Attempt int
	Meta    map[string]string
}

// Receipt reports an upstream acceptance.
type Receipt struct {
	// ProviderMessageID is the upstream's reference, used to match a later DLR.
	ProviderMessageID string
	Latency           time.Duration
	Raw               string
}

// Class tells the pipeline how to react to a failed Send.
type Class string

const (
	// ClassRetryable failures may succeed on a later attempt: timeouts, 5xx,
	// a dropped bind.
	ClassRetryable Class = "retryable"
	// ClassThrottled is a retryable failure that asks for backoff: the upstream
	// is healthy but refusing traffic. It does not count against the provider's
	// quality.
	ClassThrottled Class = "throttled"
	// ClassPermanent failures will not succeed anywhere: an invalid destination,
	// blocked content. The message fails without trying another provider.
	ClassPermanent Class = "permanent"
	// ClassProvider failures are permanent for this provider but another one may
	// succeed: bad credentials, an unsupported route, a suspended account.
	ClassProvider Class = "provider"
)

// Error is a classified delivery failure.
type Error struct {
	Class      Class
	Code       string
	Message    string
	RetryAfter time.Duration
	Cause      error
}

func (e *Error) Error() string {
	msg := e.Message
	if msg == "" && e.Cause != nil {
		msg = e.Cause.Error()
	}
	if e.Code != "" {
		return fmt.Sprintf("%s (%s): %s", e.Class, e.Code, msg)
	}
	return fmt.Sprintf("%s: %s", e.Class, msg)
}

// Unwrap returns the underlying cause.
func (e *Error) Unwrap() error { return e.Cause }

// Errorf builds a classified error.
func Errorf(class Class, code, format string, args ...any) *Error {
	return &Error{Class: class, Code: code, Message: fmt.Sprintf(format, args...)}
}

// ClassOf reports the class of err. An error that is not an *Error is treated
// as retryable: with no better information, trying again is the safe choice for
// "at least one delivery".
func ClassOf(err error) (Class, *Error) {
	var ge *Error
	if errors.As(err, &ge) {
		return ge.Class, ge
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return ClassRetryable, &Error{Class: ClassRetryable, Code: "timeout", Message: err.Error(), Cause: err}
	}
	return ClassRetryable, &Error{Class: ClassRetryable, Code: "unclassified", Message: err.Error(), Cause: err}
}

// ClassifyHTTPStatus maps an upstream HTTP status to a Class.
func ClassifyHTTPStatus(status int) Class {
	switch {
	case status == http.StatusTooManyRequests:
		return ClassThrottled
	case status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusPaymentRequired:
		return ClassProvider
	case status == http.StatusRequestTimeout:
		return ClassRetryable
	case status >= 500:
		return ClassRetryable
	case status >= 400:
		return ClassPermanent
	default:
		return ClassRetryable
	}
}

// Status is the final state a DLR reports.
type Status string

const (
	StatusDelivered Status = "delivered"
	StatusFailed    Status = "failed"
	StatusExpired   Status = "expired"
	StatusRejected  Status = "rejected"
	// StatusUnknown is an interim or unrecognised state; it never changes the
	// message.
	StatusUnknown Status = "unknown"
)

// Final reports whether the status ends the message.
func (s Status) Final() bool {
	return s == StatusDelivered || s == StatusFailed || s == StatusExpired || s == StatusRejected
}

// DLR is a delivery receipt reported by an upstream.
type DLR struct {
	// Provider is the gateway name; the gateway fills it in.
	Provider          string
	ProviderMessageID string
	// MessageID is set when the upstream echoes a client reference.
	MessageID string
	Status    Status
	Code      string
	Raw       string
	At        time.Time
}

// DLRSink receives delivery receipts. It must be safe for concurrent use and
// should return quickly: the application hands the receipt to its queue.
type DLRSink func(context.Context, DLR)

// Gateway delivers messages through one upstream.
type Gateway interface {
	// Send submits one message. It must honour ctx, and must return an *Error
	// (or an error ClassOf can interpret) on failure.
	Send(ctx context.Context, msg Message) (Receipt, error)
	// Close releases connections. It is called once.
	Close() error
}

// DLRSource is implemented by gateways that push receipts (an SMPP bind).
type DLRSource interface {
	SetDLRSink(DLRSink)
}

// DLRParser is implemented by gateways whose upstream posts receipts to a
// webhook. The application mounts the webhook and calls ParseDLR with the raw
// request body.
type DLRParser interface {
	ParseDLR(headers map[string][]string, body []byte) ([]DLR, error)
}

// Pinger is implemented by gateways that can check their upstream cheaply. The
// application uses it for the provider health report.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Factory builds a gateway from its BCL config. name is the provider name.
type Factory func(ctx context.Context, name string, config map[string]any) (Gateway, error)

var (
	mu        sync.RWMutex
	factories = map[string]Factory{}
)

// Register installs a plugin under kind. Registering twice panics: two plugins
// competing for one name is a configuration ambiguity.
func Register(kind string, f Factory) {
	if kind == "" || f == nil {
		panic("gateway: Register needs a kind and a factory")
	}
	mu.Lock()
	defer mu.Unlock()
	if _, dup := factories[kind]; dup {
		panic("gateway: duplicate plugin " + kind)
	}
	factories[kind] = f
}

// Lookup returns the factory registered for kind.
func Lookup(kind string) (Factory, bool) {
	mu.RLock()
	defer mu.RUnlock()
	f, ok := factories[kind]
	return f, ok
}

// Kinds lists the registered plugin kinds, sorted.
func Kinds() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(factories))
	for k := range factories {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Open builds a gateway of the given kind.
func Open(ctx context.Context, kind, name string, config map[string]any) (Gateway, error) {
	f, ok := Lookup(kind)
	if !ok {
		return nil, fmt.Errorf("gateway: unknown plugin kind %q (registered: %v)", kind, Kinds())
	}
	return f(ctx, name, config)
}
