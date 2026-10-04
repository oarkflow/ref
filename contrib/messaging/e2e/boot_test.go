package e2e

import "testing"

func TestApplicationDirectoryLoads(t *testing.T) {
	s := start(t)
	if status, out := s.do("GET", "/livez", "", nil); status != 200 {
		t.Fatalf("livez = %d %v", status, out)
	}
}
