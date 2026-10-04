// Package messaging collects REF drivers for messaging infrastructure that
// needs external modules: a durable queue on github.com/oarkflow/broker
// (queue.broker), phone number validation (phone.validate) and SMPP on github.com/oarkflow/smppflow (service.smpp,
// smpp.submit). Register installs them all; the generic ref in this
// module does so before it runs an application directory.
package messaging

import (
	"github.com/oarkflow/ref/contrib/messaging/phonecheck"
	"github.com/oarkflow/ref/contrib/messaging/queuebroker"
	"github.com/oarkflow/ref/contrib/messaging/sim"
	"github.com/oarkflow/ref/contrib/messaging/smpp"
)

// Register installs every driver. It is safe to call more than once.
func Register() {
	queuebroker.Register()
	smpp.Register()
	sim.Register()
	phonecheck.Register()
}
