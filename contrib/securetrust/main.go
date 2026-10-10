// Command securetrust prints the public values to build into the secure
// transport's WebAssembly client, from the server's key files:
//
//	make wasm $(go run ./contrib/securetrust -origin https://app.example.com -keys /run/secrets)
//
// or, for the example application, make wasm-trusted ORIGIN=https://app.example.com.
// Only public keys are printed; the private keys never leave their files.
package main

import (
	"crypto/ed25519"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/oarkflow/fh/mw/securetransport"
	responseprotocol "github.com/oarkflow/fh/pkg/httpsignature"
)

func main() {
	origin := flag.String("origin", "", "the exact origin users open, e.g. https://app.example.com (required)")
	keys := flag.String("keys", "examples/data-pipeline/.data/secure", "directory holding transport.key and signing.key")
	tfile := flag.String("transport-key", "", "transport key file (default <keys>/transport.key)")
	sfile := flag.String("signing-key", "", "response-signing key file (default <keys>/signing.key)")
	tid := flag.String("transport-key-id", "etl-transport-1", "key id the server announces")
	sid := flag.String("response-key-id", "etl-response-1", "response-signing key id")
	flag.Parse()
	if *origin == "" {
		fmt.Fprintln(os.Stderr, "securetrust: -origin is required")
		os.Exit(2)
	}
	if *tfile == "" {
		*tfile = filepath.Join(*keys, "transport.key")
	}
	if *sfile == "" {
		*sfile = filepath.Join(*keys, "signing.key")
	}
	read := func(path string) string {
		b, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "securetrust:", err)
			os.Exit(1)
		}
		return strings.TrimSpace(string(b))
	}
	priv, err := securetransport.DecodeServerPrivateKey(read(*tfile))
	if err != nil {
		fmt.Fprintln(os.Stderr, "securetrust:", err)
		os.Exit(1)
	}
	pub, err := securetransport.ServerPublicKey(priv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "securetrust:", err)
		os.Exit(1)
	}
	signing, err := responseprotocol.DecodePrivateKey(read(*sfile))
	if err != nil {
		fmt.Fprintln(os.Stderr, "securetrust:", err)
		os.Exit(1)
	}
	signPub, err := responseprotocol.EncodePublicKey(signing.Public().(ed25519.PublicKey))
	if err != nil {
		fmt.Fprintln(os.Stderr, "securetrust:", err)
		os.Exit(1)
	}
	fmt.Printf("WASM_TRUSTED_ORIGIN=%s WASM_TRUSTED_TRANSPORT_KEY=%s WASM_TRUSTED_TRANSPORT_KEY_ID=%s WASM_TRUSTED_RESPONSE_KEY=%s WASM_TRUSTED_RESPONSE_KEY_ID=%s\n",
		strings.TrimRight(*origin, "/"), encode(pub[:]), *tid, signPub, *sid)
}

func encode(b []byte) string {
	s, _ := securetransport.EncodeServerPrivateKey(b) // same 32-byte base64url form
	return s
}
