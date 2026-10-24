package platform

import (
	"context"
	"io"
	"testing"
)

func testFactory(tag string) ResourceFactory {
	return ResourceFactoryFunc(func(context.Context, ResourceSpec) (Resource, io.Closer, error) {
		return tag, nil, nil
	})
}

func built(t *testing.T, r *Registry, kind string) any {
	t.Helper()
	f, ok := r.resources[kind]
	if !ok {
		t.Fatalf("kind %q not registered", kind)
	}
	res, _, err := f.Open(context.Background(), ResourceSpec{Name: "x", Kind: kind})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// A fallback stands in until a real driver for the kind is registered, and the
// real driver wins whichever of the two was registered first.
func TestFallbackResourceDriverYieldsToARealDriver(t *testing.T) {
	forget := func(kind string) {
		driverMu.Lock()
		defer driverMu.Unlock()
		delete(fallbackResources, kind)
		delete(fallbackKinds, kind)
		delete(driverResources, kind)
		delete(driverKinds, kind)
	}
	for name, order := range map[string][2]string{"fallback first": {"fallback", "real"}, "real first": {"real", "fallback"}} {
		t.Run(name, func(t *testing.T) {
			kind := "test.fallback_" + name[:4]
			t.Cleanup(func() { forget(kind) })
			for _, which := range order {
				if which == "real" {
					RegisterResourceDriver(kind, testFactory("real"))
				} else {
					RegisterFallbackResourceDriver(kind, testFactory("fallback"))
				}
			}
			if got := built(t, NewRegistry(), kind); got != "real" {
				t.Fatalf("built %v, want the real driver", got)
			}
		})
	}

	t.Run("alone", func(t *testing.T) {
		const kind = "test.fallback_alone"
		t.Cleanup(func() { forget(kind) })
		RegisterFallbackResourceDriver(kind, testFactory("fallback"))
		if got := built(t, NewRegistry(), kind); got != "fallback" {
			t.Fatalf("built %v, want the fallback", got)
		}
	})

	t.Run("a second fallback panics", func(t *testing.T) {
		const kind = "test.fallback_twice"
		t.Cleanup(func() { forget(kind) })
		RegisterFallbackResourceDriver(kind, testFactory("a"))
		defer func() {
			if recover() == nil {
				t.Fatal("duplicate fallback accepted")
			}
		}()
		RegisterFallbackResourceDriver(kind, testFactory("b"))
	})
}
