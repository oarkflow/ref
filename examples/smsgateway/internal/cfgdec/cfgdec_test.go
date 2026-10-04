package cfgdec

import (
	"testing"
	"time"
)

func TestDecodeNormalisesBCLShapes(t *testing.T) {
	type rule struct {
		ID       string `json:"id"`
		Country  string `json:"country"`
		Priority int    `json:"priority"`
	}
	type cfg struct {
		Timeout Duration `json:"timeout"`
		Retry   struct {
			Initial Duration
			Max     int `json:"max"`
		} `json:"retry"`
		Rules []rule             `json:"rule"`
		Cost  map[string]float64 `json:"cost"`
	}
	in := map[string]any{
		"timeout": map[string]any{"$duration": "5s"},
		"retry":   map[string]any{"initial": map[string]any{"$duration": "250ms"}, "max": int64(3)},
		"rule": []any{
			map[string]any{"id": "a", "type": "rule", "body": map[string]any{"country": "NP", "priority": int64(10)}},
		},
		"cost": map[string]any{"NP": 0.01},
	}
	var out cfg
	if err := Decode(in, &out); err != nil {
		t.Fatal(err)
	}
	if out.Timeout.D() != 5*time.Second || out.Retry.Initial.D() != 250*time.Millisecond || out.Retry.Max != 3 {
		t.Fatalf("durations/ints wrong: %+v", out)
	}
	if len(out.Rules) != 1 || out.Rules[0].ID != "a" || out.Rules[0].Country != "NP" || out.Cost["NP"] != 0.01 {
		t.Fatalf("blocks wrong: %+v", out)
	}
}

func TestDecodeRejectsUnknownKeys(t *testing.T) {
	var out struct {
		A int `json:"a"`
	}
	if err := Decode(map[string]any{"a": int64(1), "typo": true}, &out); err == nil {
		t.Fatal("unknown key must fail")
	}
}
