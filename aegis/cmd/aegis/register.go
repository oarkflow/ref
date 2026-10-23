package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/oarkflow/ref/aegis/control"
	"github.com/oarkflow/ref/aegis/pkg"
)

// runRegister handles the `aegis register` CLI command: it registers a
// sealed package's identity and master key with a control server, so that
// server can later hand the key out as a short-lived grant (`aegis run
// -grant-server ...`) instead of the key travelling to every machine that
// runs the package as a file.
func runRegister(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("register", flag.ContinueOnError)
	flags.SetOutput(stderr)

	var (
		pkgFlag    string
		encKeyFlag string
		serverFlag string
	)
	flags.StringVar(&pkgFlag, "pkg", "", "Path to the sealed package (.apkg) to register")
	flags.StringVar(&encKeyFlag, "enckey", "", "The same AES-256 package encryption key given to `aegis package` (hex, key file, or passphrase)")
	flags.StringVar(&serverFlag, "server", "", "Base URL of the control server (e.g. https://control.example.com)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if pkgFlag == "" || encKeyFlag == "" || serverFlag == "" {
		return errors.New("aegis: register: -pkg, -enckey and -server are all required")
	}

	pkgData, err := os.ReadFile(pkgFlag)
	if err != nil {
		return fmt.Errorf("aegis: register: read package %q: %w", pkgFlag, err)
	}
	sealed, err := pkg.Unmarshal(pkgData)
	if err != nil {
		return fmt.Errorf("aegis: register: decode package %q: %w", pkgFlag, err)
	}

	// The control server must be able to hand out the exact key this
	// package was sealed with. Rather than re-deriving it from the
	// passphrase a second time here (which would need the manifest's KDF
	// info duplicated into this command for no benefit), require the
	// already-resolved key form here: resolveRunKey handles the manifest's
	// stored KDF salt the same way `aegis run` does.
	encKey, err := resolveRunKey(encKeyFlag, sealed.Manifest.KDF)
	if err != nil {
		return fmt.Errorf("aegis: register: invalid encryption key: %w", err)
	}
	// Prove the key actually opens this package before sending it to the
	// server — registering the wrong key would otherwise only surface the
	// day someone tries to run the package and gets a grant that fails.
	if _, err := pkg.Open(sealed, encKey); err != nil {
		return fmt.Errorf("aegis: register: this key does not open the package: %w", err)
	}

	payload := control.PackageRegisterPayload{
		PackageID:     sealed.Manifest.PackageID,
		ApplicationID: sealed.Manifest.ApplicationID,
		Version:       sealed.Manifest.Version,
		BuildID:       sealed.Manifest.BuildID,
		MasterKey:     hex.EncodeToString(encKey),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("aegis: register: marshal request: %w", err)
	}

	url := strings.TrimRight(serverFlag, "/") + "/v1/package/register"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("aegis: register: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("aegis: register: request to %s failed: %w", url, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("aegis: register: server returned %d: %s", resp.StatusCode, respBody)
	}

	fmt.Fprintf(stdout, "Registered package %s (%s %s) with %s\n", sealed.Manifest.PackageID, sealed.Manifest.ApplicationID, sealed.Manifest.Version, serverFlag)
	return nil
}
