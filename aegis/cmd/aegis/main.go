// Command aegis provides the command-line interface for the Aegis Runtime protected execution platform.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
)

func main() {
	if err := Run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		var ee *exitError
		if errors.As(err, &ee) {
			os.Exit(ee.Code)
		}
		os.Exit(1)
	}
}

// Run dispatches CLI arguments to the appropriate Aegis command handler.
func Run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printUsage(stdout)
		return nil
	}

	command := args[0]
	subArgs := args[1:]

	switch command {
	case "compile":
		return runCompile(subArgs, stdout, stderr)
	case "package":
		return runPackage(subArgs, stdout, stderr)
	case "inspect":
		return runInspect(subArgs, stdout, stderr)
	case "verify":
		return runVerify(subArgs, stdout, stderr)
	case "run":
		return runRun(subArgs, stdout, stderr)
	case "keygen":
		return runKeygen(subArgs, stdout, stderr)
	case "respond-keygen":
		return runRespondKeygen(subArgs, stdout, stderr)
	case "respond-sign":
		return runRespondSign(subArgs, stdout, stderr)
	case "lab":
		return runLab(subArgs, stdout, stderr)
	case "serve":
		return runServe(subArgs, stdout, stderr)
	case "register":
		return runRegister(subArgs, stdout, stderr)
	case "revoke":
		return runRevoke(subArgs, stdout, stderr)
	case "help", "-h", "--help":
		printUsage(stdout)
		return nil
	case "version", "-v", "--version":
		fmt.Fprintln(stdout, "aegis version 1.0.0")
		return nil
	default:
		printUsage(stderr)
		return fmt.Errorf("aegis: unknown command %q", command)
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintf(w, `Aegis Runtime CLI - Protected Execution Platform

Usage:
  aegis <command> [options]

Commands:
  keygen   Generate Ed25519 signing key pair and 32-byte AES package encryption key
           aegis keygen -out <prefix>

  compile  Compile Go source functions into an Aegis Intermediate Representation (.air)
           aegis compile -src <source.go> -entry <func1,func2> -out <module.air>

  package  Encrypt, seal, and sign an IR module or REF application directory into a protected package (.apkg)
           aegis package -dir <appDir> -key <signing.key> -enckey <hex/pass> -out <app.apkg>
           aegis package -module <module.air> -key <signing.key> -enckey <hex/pass> -out <release.apkg>

  inspect  Inspect metadata, integrity hashes, and policies inside an Aegis package
           aegis inspect -pkg <release.apkg>

  verify   Cryptographically verify an Aegis package signature against a trusted public key
           aegis verify -pkg <release.apkg> -pubkey <pubkey.pem|jwks>

  run      Run a REF application or execute an exported function in the Aegis Secure Platform
           aegis run <appDir> [-addr :8080]
           aegis run <app.apkg> -key <enckey-hex> -pubkey <pub.pem> [-addr :8080]
           aegis run <app.apkg> -grant-server <url> -pubkey <pub.pem> [-addr :8080]
           aegis run -pkg <release.apkg> -key <enckey-hex> -pubkey <pub.pem> -func <funcName> -args <1,2,3>

  respond-keygen  Generate the Ed25519 key that signs escalation policies for 'run -respond=on'
           aegis respond-keygen -out <prefix>

  respond-sign    Sign an escalation policy (flags override the defaults; -key from respond-keygen)
           aegis respond-sign -key <prefix.key> -out <policy.json> [-thresholds 0.3,0.8,1.6,2.5] [-max-regenerations 3]

  lab      Benchmark reverse-engineering recovery resistance and instruction metrics
           aegis lab -orig <original.air> [-prot <protected.air>] [-func <name> -args <1,2>]

  serve    Start the Aegis Control Plane authorization and grant server
           aegis serve -addr :8080 -key <signing.key> -ttl 3600 -store control.db

  register Register a sealed package's identity and key with a control server
           aegis register -pkg <release.apkg> -enckey <hex/pass> -server <url>

  revoke   Tell a control server to refuse every further grant for a package
           aegis revoke -package-id <id> -reason <why> -server <url>

  help     Show this help message
  version  Show Aegis CLI version
`)
}
