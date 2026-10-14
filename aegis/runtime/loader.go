package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/oarkflow/ref/aegis/ir"
	"github.com/oarkflow/ref/aegis/pkg"
)

// LoadedPackage represents a verified, sealed package loaded into the runtime.
// The inner IR module is decrypted and populated upon successful authorization.
type LoadedPackage struct {
	mutex    sync.RWMutex
	pkg      *pkg.Package
	manifest *pkg.Manifest
	module   *ir.Module
	grant    *Grant
}

// Package returns the underlying raw sealed package.
func (lp *LoadedPackage) Package() *pkg.Package {
	if lp == nil {
		return nil
	}
	lp.mutex.RLock()
	defer lp.mutex.RUnlock()
	return lp.pkg
}

// Manifest returns the verified package manifest.
func (lp *LoadedPackage) Manifest() *pkg.Manifest {
	if lp == nil {
		return nil
	}
	lp.mutex.RLock()
	defer lp.mutex.RUnlock()
	return lp.manifest
}

// Module returns the decrypted IR module if authorized, or nil otherwise.
func (lp *LoadedPackage) Module() *ir.Module {
	if lp == nil {
		return nil
	}
	lp.mutex.RLock()
	defer lp.mutex.RUnlock()
	return lp.module
}

// Grant returns the active authorization grant if authorized, or nil otherwise.
func (lp *LoadedPackage) Grant() *Grant {
	if lp == nil {
		return nil
	}
	lp.mutex.RLock()
	defer lp.mutex.RUnlock()
	return lp.grant
}

// IsAuthorized reports whether the package is currently authorized and its grant has not expired.
func (lp *LoadedPackage) IsAuthorized() bool {
	if lp == nil {
		return false
	}
	lp.mutex.RLock()
	defer lp.mutex.RUnlock()
	return lp.module != nil && lp.grant != nil && !lp.grant.IsExpired(time.Now())
}

// LoadPackage unmarshals a binary container package, verifies its cryptographic signature,
// checks manifest hashes, validates required capabilities, and returns a LoadedPackage.
func (r *Runtime) LoadPackage(ctx context.Context, data []byte) (*LoadedPackage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	r.mu.RLock()
	closed := r.closed
	reader := r.reader
	caps := r.config.Capabilities
	r.mu.RUnlock()

	if closed {
		return nil, errors.New("runtime: runtime is closed")
	}

	if reader == nil {
		return nil, errors.New("runtime: package reader not initialized")
	}

	// 1. Unmarshal package binary container
	p, err := pkg.Unmarshal(data)
	if err != nil {
		return nil, fmt.Errorf("runtime: failed to unmarshal package: %w", err)
	}

	// 2. Cryptographically verify signature and integrity
	if err := reader.Verify(p); err != nil {
		return nil, fmt.Errorf("runtime: package verification failed: %w", err)
	}

	// 3. Verify required capabilities
	for _, requiredCap := range p.Manifest.RequiredCapabilities {
		if !hasCapability(caps, requiredCap) {
			return nil, fmt.Errorf("runtime: missing required capability %q", requiredCap)
		}
	}

	manifestCopy := p.Manifest
	return &LoadedPackage{
		pkg:      p,
		manifest: &manifestCopy,
	}, nil
}

// Load verifies an already unmarshaled Package structure against the runtime's trusted keys.
func (r *Runtime) Load(ctx context.Context, p *pkg.Package) (*LoadedPackage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p == nil {
		return nil, errors.New("runtime: package cannot be nil")
	}

	r.mu.RLock()
	closed := r.closed
	reader := r.reader
	caps := r.config.Capabilities
	r.mu.RUnlock()

	if closed {
		return nil, errors.New("runtime: runtime is closed")
	}

	if reader == nil {
		return nil, errors.New("runtime: package reader not initialized")
	}

	if err := reader.Verify(p); err != nil {
		return nil, fmt.Errorf("runtime: package verification failed: %w", err)
	}

	for _, requiredCap := range p.Manifest.RequiredCapabilities {
		if !hasCapability(caps, requiredCap) {
			return nil, fmt.Errorf("runtime: missing required capability %q", requiredCap)
		}
	}

	manifestCopy := p.Manifest
	return &LoadedPackage{
		pkg:      p,
		manifest: &manifestCopy,
	}, nil
}

func hasCapability(caps []string, required string) bool {
	for _, c := range caps {
		if c == required {
			return true
		}
	}
	return false
}
