// Command smsgateway runs the SMS application: a REF platform whose pipelines,
// providers, routing and pricing are the BCL in ./app.
//
//	go run . --sandbox          # everything in-process: SMSC, vendor API, demo keys
//	go run . --config ./app     # your providers, from your BCL and environment
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/oarkflow/fh"

	"github.com/oarkflow/ref/examples/smsgateway/internal/app"
	"github.com/oarkflow/ref/examples/smsgateway/internal/sandbox"
)

func main() {
	if err := run(); err != nil {
		slog.Error("smsgateway", "error", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		dir     = flag.String("config", "app", "directory of .bcl files")
		listen  = flag.String("listen", getenv("SMS_LISTEN", ":8089"), "HTTP listen address")
		sandbx  = flag.Bool("sandbox", false, "run an SMPP SMSC and a vendor API in-process and issue demo API keys")
		dataDir = flag.String("data", ".data/sms", "directory for the default database and broker log")
	)
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := os.MkdirAll(*dataDir, 0o750); err != nil {
		return err
	}
	if _, ok := os.LookupEnv("SMS_DSN"); !ok {
		_ = os.Setenv("SMS_DSN", "file:"+*dataDir+"/sms.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	}
	if _, ok := os.LookupEnv("SMS_BROKER_DIR"); !ok {
		_ = os.Setenv("SMS_BROKER_DIR", *dataDir+"/broker")
	}

	adminKey := os.Getenv("SMS_ADMIN_KEY")
	generated := false
	if adminKey == "" {
		if os.Getenv("APP_ENV") == "production" {
			return errors.New("SMS_ADMIN_KEY is required in production")
		}
		adminKey, generated = randomKey("smsadm_"), true
		_ = os.Setenv("SMS_ADMIN_KEY", adminKey)
	}

	var (
		smsc   *sandbox.SMSC
		vendor *sandbox.Vendor
	)
	if *sandbx {
		var err error
		if smsc, err = sandbox.StartSMSC(sandbox.SMSCConfig{SystemID: "smsgw", Password: "sandbox"}); err != nil {
			return err
		}
		defer smsc.Close()
		if vendor, err = sandbox.StartVendor("sandbox-token"); err != nil {
			return err
		}
		defer vendor.Close()
		_ = os.Setenv("NP_SMPP_ADDR", smsc.Addr())
		_ = os.Setenv("IN_VENDOR_URL", vendor.URL()+"/send")
	}

	a, err := app.Load(ctx, app.Options{Dir: *dir})
	if err != nil {
		return err
	}
	srv := fh.NewFast()
	if err := a.Mount(srv); err != nil {
		_ = a.Close()
		return err
	}

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		_ = a.Close()
		return err
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	base := "http://" + ln.Addr().String()
	if host, port, err := net.SplitHostPort(ln.Addr().String()); err == nil && (host == "::" || host == "0.0.0.0" || host == "") {
		base = "http://127.0.0.1:" + port
	}

	// Banner on stdout, never in the structured log: keys must not reach a log
	// sink.
	fmt.Printf("\nSMS gateway listening on %s\n", base)
	if generated {
		fmt.Printf("  operator key (generated, shown once): %s\n", adminKey)
	}
	if *sandbx {
		vendor.SetCallback(base+"/v1/webhooks/dlr/in_vendor", getenv("IN_VENDOR_WEBHOOK_SECRET", "sandbox-secret"))
		fmt.Printf("  sandbox SMSC %s, vendor API %s\n", smsc.Addr(), vendor.URL())
		for _, u := range []string{"demo", "acme_alice", "acme_bob"} {
			if key, err := a.Hub.IssueKey(ctx, u); err == nil {
				fmt.Printf("  %-11s API key: %s\n", u, key)
			}
		}
		fmt.Printf("\n  curl -s %s/v1/messages -H 'Authorization: Bearer <demo key>' -H 'Content-Type: application/json' \\\n       -d '{\"to\":\"+9779841234567\",\"text\":\"Hello\",\"type\":\"otp\"}'\n", base)
	}
	fmt.Println()

	select {
	case <-ctx.Done():
	case err := <-served:
		if err != nil {
			_ = a.Close()
			return err
		}
	}
	slog.Info("shutting down")
	_ = srv.ShutdownWithTimeout(10 * time.Second)
	return a.Close()
}

func getenv(k, def string) string {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		return v
	}
	return def
}

func randomKey(prefix string) string {
	var b [20]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}
