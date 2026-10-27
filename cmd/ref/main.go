// Command ref runs a REF application directory: BCL, rules, SPL templates,
// static files, authentication, sessions, workflows and workers, with no
// application code.
//
//	ref ./examples/smsgateway
//	ref -addr :9000 ./examples/boilerplate
//
// It carries every driver in this repository, including the messaging ones in
// contrib/messaging (queue.broker, service.smpp, smpp.submit and the sandbox
// stand-ins) and the calls drivers in contrib/rtc (rtc.calls, rtc.sfu, call.*).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/oarkflow/ref/contrib/aegis"
	"github.com/oarkflow/ref/contrib/messaging"
	"github.com/oarkflow/ref/contrib/rtc"
	"github.com/oarkflow/ref/serve"
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "validate" || os.Args[1] == "show") {
		messaging.Register()
		rtc.Register()
		aegis.Register()
		if err := runValidation(context.Background(), os.Args[1], os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "ref:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && (os.Args[1] == "migrate" || os.Args[1] == "seed") {
		if err := runMigration(context.Background(), os.Args[1], os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "ref:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "user" {
		if err := runUser(context.Background(), os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "ref:", err)
			os.Exit(1)
		}
		return
	}
	addr := flag.String("addr", "", "listen address (default :$PORT or :8080)")
	env := flag.String("env", "", "environment name (default $APP_ENV or development)")
	studioAddr := flag.String("studio", "", "also serve REF Studio, the visual editor, on this address (e.g. :8081)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: ref [-addr :8080] [-env development] [-studio :8081] <application directory>\n       ref validate <application directory>\n       ref show <application directory>\n       ref migrate <application directory>\n       ref seed <application directory>\n       ref user create|list [flags] <application directory>\n")
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	messaging.Register()
	rtc.Register()
	aegis.Register()
	var host *studioHost
	if *studioAddr != "" {
		var err error
		host, err = startStudio(context.Background(), flag.Arg(0), *studioAddr)
		if err != nil {
			fmt.Fprintln(os.Stderr, "ref:", err)
			os.Exit(1)
		}
		defer host.Close()
	}
	opts := serve.Options{Dir: flag.Arg(0), Addr: *addr, Env: *env}
	if host != nil {
		opts.OnPlatform = host.plat.Store
	}
	if err := serve.Run(context.Background(), opts); err != nil {
		fmt.Fprintln(os.Stderr, "ref:", err)
		os.Exit(1)
	}
}
