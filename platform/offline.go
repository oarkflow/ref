package platform

import (
	"context"
	"net/http"

	"github.com/oarkflow/ref/platform/spi"
)

// Offline compiles.
//
// A host that wants to run an application without letting it reach the outside
// world — Studio's live preview is the case in point — compiles it with a
// context from WithOffline. The built-in outbound providers (service.http,
// service.llm through it, and service.smtp) then use the supplied stand-ins
// instead of dialling: configuration is still parsed and the allowlists still
// enforced, so a mistake in the document still shows up, but no DNS lookup, no
// connection and no client certificate read happens.
//
// The context is consulted only while resources open. A context without an
// offline environment behaves exactly as before.

type offlineKey struct{}

type offlineEnv struct {
	transport http.RoundTripper
	mailer    spi.Mailer
}

// WithOffline returns a context under which service.http (and service.llm)
// send through transport and service.smtp sends through mailer. Either may be
// nil to leave that provider unchanged.
func WithOffline(ctx context.Context, transport http.RoundTripper, mailer spi.Mailer) context.Context {
	return context.WithValue(ctx, offlineKey{}, &offlineEnv{transport: transport, mailer: mailer})
}

func offlineFrom(ctx context.Context) (*offlineEnv, bool) {
	if ctx == nil {
		return nil, false
	}
	env, ok := ctx.Value(offlineKey{}).(*offlineEnv)
	return env, ok && env != nil
}

// offlineMailer keeps the resource's configured default From address, like the
// real provider, and hands the message to the stand-in.
type offlineMailer struct {
	from   string
	mailer spi.Mailer
}

func (m offlineMailer) Send(ctx context.Context, msg spi.Mail) (string, error) {
	if msg.From == "" {
		msg.From = m.from
	}
	return m.mailer.Send(ctx, msg)
}
