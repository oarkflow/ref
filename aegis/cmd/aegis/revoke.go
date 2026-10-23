package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/oarkflow/ref/aegis/control"
)

// runRevoke handles the `aegis revoke` CLI command: it tells a control
// server to refuse any further grant for a package id, the CLI-side
// counterpart to `aegis register` (POST /v1/package/register), both already
// implemented server-side in aegis/control but previously reachable only
// by hand-building the HTTP request.
func runRevoke(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("revoke", flag.ContinueOnError)
	flags.SetOutput(stderr)

	var (
		packageIDFlag string
		reasonFlag    string
		serverFlag    string
	)
	flags.StringVar(&packageIDFlag, "package-id", "", "The package id to revoke (as printed by `aegis inspect` or `aegis package`)")
	flags.StringVar(&reasonFlag, "reason", "", "Why this package is being revoked; recorded and returned to anyone who checks its status")
	flags.StringVar(&serverFlag, "server", "", "Base URL of the control server (e.g. https://control.example.com)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if packageIDFlag == "" || serverFlag == "" {
		return errors.New("aegis: revoke: -package-id and -server are both required")
	}

	payload := control.PackageRevokePayload{PackageID: packageIDFlag, Reason: reasonFlag}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("aegis: revoke: marshal request: %w", err)
	}

	url := strings.TrimRight(serverFlag, "/") + "/v1/package/revoke"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("aegis: revoke: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("aegis: revoke: request to %s failed: %w", url, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("aegis: revoke: server returned %d: %s", resp.StatusCode, respBody)
	}

	fmt.Fprintf(stdout, "Revoked package %s with %s\n", packageIDFlag, serverFlag)
	if reasonFlag != "" {
		fmt.Fprintf(stdout, "  Reason: %s\n", reasonFlag)
	}
	return nil
}
