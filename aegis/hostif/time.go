package hostif

import (
	"context"
	"errors"
	"time"

	"github.com/oarkflow/ref/aegis/vm"
)

// TimeNowUnixHostFunc constructs the "host.time_now_unix" host function returning unix epoch seconds.
func TimeNowUnixHostFunc(fallbackCarrier *Carrier) vm.HostFunc {
	return vm.HostFunc{
		Module: "host",
		Name:   "time_now_unix",
		Func: func(ctx context.Context, v *vm.VM, args []uint64) ([]uint64, error) {
			carrier, ok := CarrierFromContext(ctx)
			if !ok {
				carrier = fallbackCarrier
			}

			now := time.Now()
			if carrier != nil {
				now = carrier.Now()
			}

			return []uint64{uint64(now.Unix())}, nil
		},
	}
}

// TimeNowNanosHostFunc constructs the "host.time_now_nanos" host function returning unix epoch nanoseconds.
func TimeNowNanosHostFunc(fallbackCarrier *Carrier) vm.HostFunc {
	return vm.HostFunc{
		Module: "host",
		Name:   "time_now_nanos",
		Func: func(ctx context.Context, v *vm.VM, args []uint64) ([]uint64, error) {
			carrier, ok := CarrierFromContext(ctx)
			if !ok {
				carrier = fallbackCarrier
			}

			now := time.Now()
			if carrier != nil {
				now = carrier.Now()
			}

			return []uint64{uint64(now.UnixNano())}, nil
		},
	}
}

// RegisterTime registers the standard time host functions (time_now_unix, time_now_nanos) in reg.
func RegisterTime(reg *Registry, policies ...Policy) error {
	if reg == nil {
		return errors.New("hostif: registry is nil")
	}

	policy := Policy{Capability: CapTime}
	if len(policies) > 0 {
		policy = policies[0]
		if policy.Capability == "" {
			policy.Capability = CapTime
		}
	}

	if err := reg.Register(TimeNowUnixHostFunc(reg.Carrier()), policy); err != nil {
		return err
	}
	if err := reg.Register(TimeNowNanosHostFunc(reg.Carrier()), policy); err != nil {
		return err
	}
	return nil
}
