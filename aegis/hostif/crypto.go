package hostif

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"

	"github.com/oarkflow/ref/aegis/vm"
)

var cryptoScratchPool = sync.Pool{
	New: func() any {
		buf := make([]byte, 256)
		return &buf
	},
}

// HashSHA256HostFunc constructs the "host.hash_sha256" host function.
//
// Calling conventions supported:
//   - 3 args: hash_sha256(src_ptr, src_len, dest_ptr) -> computes SHA-256 and writes 32 bytes to dest_ptr
//   - 2 args: hash_sha256(src_ptr, src_len) -> computes SHA-256 and saves digest in context Carrier
func HashSHA256HostFunc() vm.HostFunc {
	return vm.HostFunc{
		Module: "host",
		Name:   "hash_sha256",
		Func: func(ctx context.Context, v *vm.VM, args []uint64) ([]uint64, error) {
			if v == nil {
				return nil, ErrNilVM
			}
			if len(args) < 2 {
				return nil, fmt.Errorf("hostif: hash_sha256 requires at least 2 arguments (src_ptr, src_len), got %d", len(args))
			}

			srcPtr := uint32(args[0])
			srcLen := uint32(args[1])

			data, err := v.ReadMemory(srcPtr, srcLen)
			if err != nil {
				return nil, fmt.Errorf("hostif: hash_sha256 memory read failed: %w", err)
			}

			digest := sha256.Sum256(data)

			if len(args) >= 3 {
				destPtr := uint32(args[2])
				if err := v.WriteMemory(destPtr, digest[:]); err != nil {
					return nil, fmt.Errorf("hostif: hash_sha256 memory write failed: %w", err)
				}
				return []uint64{32}, nil
			}

			// If no destination pointer was supplied, store in Carrier if available
			if carrier, ok := CarrierFromContext(ctx); ok && carrier != nil {
				carrier.SetLastHash(digest[:])
			}
			return []uint64{0}, nil
		},
	}
}

// RandomBytesHostFunc constructs the "host.random_bytes" host function.
//
// Calling convention:
//   - 2 args: random_bytes(dest_ptr, length) -> writes cryptographically secure random bytes
func RandomBytesHostFunc() vm.HostFunc {
	return vm.HostFunc{
		Module: "host",
		Name:   "random_bytes",
		Func: func(ctx context.Context, v *vm.VM, args []uint64) ([]uint64, error) {
			if v == nil {
				return nil, ErrNilVM
			}
			if len(args) < 2 {
				return nil, fmt.Errorf("hostif: random_bytes requires 2 arguments (dest_ptr, length), got %d", len(args))
			}

			destPtr := uint32(args[0])
			length := uint32(args[1])

			if length == 0 {
				return []uint64{0}, nil
			}

			var buf []byte
			if length <= 256 {
				pooled := cryptoScratchPool.Get().(*[]byte)
				buf = (*pooled)[:length]
				defer cryptoScratchPool.Put(pooled)
			} else {
				buf = make([]byte, length)
			}

			if _, err := rand.Read(buf); err != nil {
				return nil, fmt.Errorf("hostif: random_bytes failed to read secure entropy: %w", err)
			}

			if err := v.WriteMemory(destPtr, buf); err != nil {
				return nil, fmt.Errorf("hostif: random_bytes failed to write memory: %w", err)
			}

			return []uint64{uint64(length)}, nil
		},
	}
}

// RegisterCrypto registers the standard crypto host functions (hash_sha256, random_bytes) in reg.
func RegisterCrypto(reg *Registry, policies ...Policy) error {
	if reg == nil {
		return errors.New("hostif: registry is nil")
	}

	policy := Policy{Capability: CapCrypto}
	if len(policies) > 0 {
		policy = policies[0]
		if policy.Capability == "" {
			policy.Capability = CapCrypto
		}
	}

	if err := reg.Register(HashSHA256HostFunc(), policy); err != nil {
		return err
	}
	if err := reg.Register(RandomBytesHostFunc(), policy); err != nil {
		return err
	}
	return nil
}
