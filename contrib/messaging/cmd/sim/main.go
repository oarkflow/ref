// Command sim runs stand-ins for the upstreams a messaging application talks
// to: an SMPP server (smppflow's) that answers submit_sm and sends delivery
// receipts, and a vendor HTTP API (POST /send) that posts receipts to a
// callback URL. It exists so an application can be run end to end with nothing
// external.
//
//	sim -smpp :2775 -vendor :9100 -callback http://127.0.0.1:8080/v1/webhooks/dlr/in_vendor
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/oarkflow/ref/contrib/messaging/sim"
)

func main() {
	smppAddr := flag.String("smpp", "127.0.0.1:2775", "SMPP listen address")
	system := flag.String("system-id", "smsgw", "SMPP system id")
	password := flag.String("password", "sandbox", "SMPP password")
	vendorAddr := flag.String("vendor", "127.0.0.1:9100", "vendor API listen address")
	token := flag.String("token", "sandbox-token", "vendor API bearer token")
	callback := flag.String("callback", "", "URL the vendor posts receipts to")
	secret := flag.String("secret", "webhook-secret-change-me", "X-Webhook-Secret the vendor sends")
	flag.Parse()

	smsc, err := sim.StartSMSC(sim.SMSCConfig{Addr: *smppAddr, SystemID: *system, Password: *password})
	if err != nil {
		fmt.Fprintln(os.Stderr, "sim:", err)
		os.Exit(1)
	}
	defer smsc.Close()
	vendor, err := sim.StartVendorOn(*vendorAddr, *token)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sim:", err)
		os.Exit(1)
	}
	defer vendor.Close()
	vendor.SetCallback(*callback, *secret)
	fmt.Printf("SMPP server on %s (system_id %s), vendor API on %s\n", smsc.Addr(), *system, vendor.URL())
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	<-c
}
