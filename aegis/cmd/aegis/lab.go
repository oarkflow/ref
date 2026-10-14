package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/oarkflow/ref/aegis/ir"
	"github.com/oarkflow/ref/aegis/lab"
	"github.com/oarkflow/ref/aegis/transform"
)

func runLab(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("lab", flag.ContinueOnError)
	fs.SetOutput(stderr)

	origPath := fs.String("orig", "", "Path to original unprotected Aegis IR module (.air)")
	protPath := fs.String("prot", "", "Path to protected Aegis IR module (.air) (optional, if omitted standard pipeline is applied to orig)")
	funcName := fs.String("func", "", "Name of exported function to benchmark (optional)")
	argsStr := fs.String("args", "", "Comma-separated arguments for test case (e.g. 50,10,20)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *origPath == "" {
		return fmt.Errorf("aegis: lab: -orig flag is required")
	}

	origData, err := os.ReadFile(*origPath)
	if err != nil {
		return fmt.Errorf("aegis: lab: failed to read original module %q: %w", *origPath, err)
	}

	origMod, err := ir.Decode(origData)
	if err != nil {
		return fmt.Errorf("aegis: lab: failed to decode original module: %w", err)
	}

	var protMod *ir.Module
	if *protPath != "" {
		protData, err := os.ReadFile(*protPath)
		if err != nil {
			return fmt.Errorf("aegis: lab: failed to read protected module %q: %w", *protPath, err)
		}
		protMod, err = ir.Decode(protData)
		if err != nil {
			return fmt.Errorf("aegis: lab: failed to decode protected module: %w", err)
		}
	} else {
		// Apply standard protection pipeline automatically
		pipeline := transform.StandardPipeline()
		var pErr error
		protMod, _, pErr = pipeline.Apply(origMod)
		if pErr != nil {
			return fmt.Errorf("aegis: lab: failed to apply transformation pipeline: %w", pErr)
		}
	}

	// Prepare test case if args provided
	var testCases []lab.TestCase
	if *funcName != "" && *argsStr != "" {
		parts := strings.Split(*argsStr, ",")
		callArgs := make([]uint64, 0, len(parts))
		for _, p := range parts {
			val, pErr := strconv.ParseUint(strings.TrimSpace(p), 10, 64)
			if pErr != nil {
				return fmt.Errorf("aegis: lab: invalid numerical argument %q: %w", p, pErr)
			}
			callArgs = append(callArgs, val)
		}
		testCases = append(testCases, lab.TestCase{
			Function: *funcName,
			Args:     callArgs,
		})
	}

	bench := &lab.RecoveryBenchmark{
		Name:      "Aegis Obfuscation Resistance Benchmark",
		Original:  origMod,
		Protected: protMod,
		TestCases: testCases,
	}

	report, err := lab.RunBenchmark(context.Background(), bench)
	if err != nil {
		return fmt.Errorf("aegis: lab: benchmark failed: %w", err)
	}

	fmt.Fprintln(stdout, report.Markdown())
	return nil
}
