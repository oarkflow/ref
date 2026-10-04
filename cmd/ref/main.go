// Command ref runs a REF application directory: BCL, rules, SPL templates,
// static files, authentication, sessions, workflows and workers, with no
// application code.
//
//	ref ./examples/smsgateway
//	ref -addr :9000 ./examples/boilerplate
//
// It carries every driver in this repository, including the messaging ones in
// contrib/messaging (queue.broker, service.smpp, smpp.submit and the sandbox
// stand-ins).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/oarkflow/ref/contrib/messaging"
	"github.com/oarkflow/ref/serve"
)

func main() {
	addr := flag.String("addr", "", "listen address (default :$PORT or :8080)")
	env := flag.String("env", "", "environment name (default $APP_ENV or development)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: ref [-addr :8080] [-env development] <application directory>\n")
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	messaging.Register()
	if err := serve.Run(context.Background(), serve.Options{Dir: flag.Arg(0), Addr: *addr, Env: *env}); err != nil {
		fmt.Fprintln(os.Stderr, "ref:", err)
		os.Exit(1)
	}
}
