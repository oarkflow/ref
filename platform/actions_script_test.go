package platform

import (
	"context"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/oarkflow/ref/runtime"
)

func testScripts(t *testing.T, specs ...ScriptSpec) *Platform {
	t.Helper()
	sc, err := compileScripts(specs, fstest.MapFS{"s/gate.spl": {Data: []byte(`{"passed": data.amount <= 100, "stage": stage, "case": kase.id}`)}})
	if err != nil {
		t.Fatal(err)
	}
	return &Platform{Engine: &runtime.Engine{}, scripts: sc}
}

func runScriptNode(t *testing.T, p *Platform, provides []string, inputs map[string]any) (map[string]any, error) {
	t.Helper()
	act, err := scriptAction(BuildContext{Platform: p}, NodeSpec{Name: "n", Uses: "script", Provides: provides, Config: map[string]any{"script": "main"}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := act.Run(&ActionContext{Context: context.Background(), Inputs: inputs, Now: time.Now()})
	return res.Outputs, err
}

func TestScriptNodeComputesOverFacts(t *testing.T) {
	p := testScripts(t, ScriptSpec{Name: "main", Source: `
		let total = 0;
		for (let i = 0; i < len(lines); i = i + 1) { total = total + lines[i].qty * lines[i].price; }
		{"total": total, "big": total > 100}
	`})
	out, err := runScriptNode(t, p, []string{"order"}, map[string]any{"lines": []any{
		map[string]any{"qty": 2, "price": 30}, map[string]any{"qty": 1, "price": 55},
	}})
	if err != nil {
		t.Fatal(err)
	}
	got := out["order"].(map[string]any)
	if got["total"] != int64(115) || got["big"] != true {
		t.Fatalf("got %#v", got)
	}
}

func TestScriptNodeSplitsObjectAcrossProvides(t *testing.T) {
	p := testScripts(t, ScriptSpec{Name: "main", Source: `{"a": n + 1, "b": "x"}`})
	out, err := runScriptNode(t, p, []string{"a", "b"}, map[string]any{"n": 1})
	if err != nil || out["a"] != int64(2) || out["b"] != "x" {
		t.Fatalf("%#v %v", out, err)
	}
}

func TestScriptsAreSandboxed(t *testing.T) {
	for name, src := range map[string]string{
		"read file": `read_file("/etc/passwd")[0]`,
		"exec":      `exec("echo", "hi", 1000)`,
		"env":       `os_env("HOME")`,
		"import":    `import "os"; 1`,
		"loop":      `while (true) {}`,
	} {
		p := testScripts(t, ScriptSpec{Name: "main", Source: src, MaxSteps: 50000})
		out, err := runScriptNode(t, p, []string{"r"}, nil)
		if err == nil && out["r"] != nil {
			t.Errorf("%s: sandbox let it through: %#v", name, out)
		}
	}
	p := testScripts(t, ScriptSpec{Name: "main", Source: `read_file("actions_script.go")[0]`})
	out, _ := runScriptNode(t, p, []string{"r"}, nil)
	if s, _ := out["r"].(string); strings.Contains(s, "package platform") {
		t.Fatal("script read a host file")
	}
}

func TestScriptNodeConcurrent(t *testing.T) {
	p := testScripts(t, ScriptSpec{Name: "main", Source: `n * 2`})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out, err := runScriptNode(t, p, []string{"r"}, map[string]any{"n": i})
			if err != nil || out["r"] != int64(i*2) {
				t.Errorf("%d: %#v %v", i, out, err)
			}
		}(i)
	}
	wg.Wait()
}

func TestScriptBlocksFailAtLoad(t *testing.T) {
	fsys := fstest.MapFS{"ok.spl": {Data: []byte("1")}, "bad.spl": {Data: []byte("let = ;")}}
	for name, specs := range map[string][]ScriptSpec{
		"no body":      {{Name: "a"}},
		"both":         {{Name: "a", Source: "1", File: "ok.spl"}},
		"duplicate":    {{Name: "a", Source: "1"}, {Name: "a", Source: "2"}},
		"missing file": {{Name: "a", File: "nope.spl"}},
		"not spl":      {{Name: "a", File: "x.txt"}},
		"syntax":       {{Name: "a", File: "bad.spl"}},
		"bad timeout":  {{Name: "a", Source: "1", Timeout: "soon"}},
	} {
		if _, err := compileScripts(specs, fsys); err == nil {
			t.Errorf("%s: must fail at load", name)
		}
	}
	if _, err := compileScripts([]ScriptSpec{{Name: "a", File: "ok.spl", Timeout: "1s"}}, fsys); err != nil {
		t.Fatal(err)
	}
	if _, err := scriptAction(BuildContext{Platform: testScripts(t)}, NodeSpec{Name: "n", Provides: []string{"r"}, Config: map[string]any{"script": "nope"}}); err == nil {
		t.Error("a node naming an unknown script must fail at build time")
	}
}

func TestScriptHookByID(t *testing.T) {
	p := testScripts(t, ScriptSpec{Name: "gate", File: "s/gate.spl"})
	input := map[string]any{"case": map[string]any{"id": "c1"}, "data": map[string]any{"amount": 250}, "stage": "review"}
	out, err := p.CallIntent(context.Background(), "script:gate", input, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := out.(map[string]any)
	if got["passed"] != false || got["stage"] != "review" || got["case"] != "c1" {
		t.Fatalf("got %#v", got)
	}
	input["data"] = map[string]any{"amount": 40}
	out, _ = p.CallIntent(context.Background(), "script:gate", input, nil)
	if out.(map[string]any)["passed"] != true {
		t.Fatalf("got %#v", out)
	}
	if _, err := p.CallIntent(context.Background(), "script:missing", input, nil); err == nil {
		t.Error("an unknown script id must fail")
	}
	if err := checkScriptHook(map[string]bool{"gate": true}, "script:nope"); err == nil {
		t.Error("load-time check must reject an undeclared script")
	}
	if err := checkScriptHook(map[string]bool{"gate": true}, "script:gate"); err != nil {
		t.Error(err)
	}
}

func TestScriptHeapLimitIsGrowthNotProcessHeap(t *testing.T) {
	// A large heap already in use must not trip a small per-script allowance.
	ballast := make([][]byte, 0, 64)
	for i := 0; i < 64; i++ {
		ballast = append(ballast, make([]byte, 4<<20)) // 256MB resident
		ballast[i][0] = 1
	}
	defer func() { _ = ballast }()

	const hog = `
		let rows = [];
		for (let i = 0; i < 90000; i = i + 1) { rows.push({"id": i, "name": sprintf("row-%d", i), "pad": "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}); }
		len(rows)`
	p := testScripts(t,
		ScriptSpec{Name: "small", Source: `1 + 1`, MaxHeapMB: 8},
		ScriptSpec{Name: "hog_limited", Source: hog, MaxHeapMB: 4, MaxSteps: 50_000_000},
		ScriptSpec{Name: "hog_open", Source: hog, MaxSteps: 50_000_000},
	)
	run := func(id string) error {
		_, err := p.scripts[id].Run(map[string]any{})
		return err
	}
	if err := run("small"); err != nil {
		t.Fatalf("small script under a heap limit: %v", err)
	}
	if err := run("hog_limited"); err == nil || !strings.Contains(err.Error(), "heap") {
		t.Fatalf("a script growing the heap past its allowance must stop, got %v", err)
	}
	if err := run("hog_open"); err != nil {
		t.Fatalf("without max_heap_mb there is no heap limit: %v", err)
	}
}
