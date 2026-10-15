package platform

// ScriptSpec is a named SPL script. Declare it once and refer to it by name
// from a `script` node (`config { script "name" }`) or from any pipeline hook
// (`hook "script:name"`, `on_enter "script:name"`).
//
//	script "approval_limit" {
//	  description "Fails an approval over the facility limit"
//	  file "scripts/approval_limit.spl"   # or: source "..."
//	  timeout "1s"
//	}
type ScriptSpec struct {
	Name        string `bcl:",id"`
	Description string `bcl:"description"`
	// Source is inline SPL. File names an .spl file in the application. Exactly
	// one is set.
	Source string `bcl:"source"`
	File   string `bcl:"file"`
	// Timeout and MaxSteps bound one run; zero takes the defaults (2s,
	// 1,000,000 steps). String, array and hash sizes are always capped.
	// MaxHeapMB is how much the process heap may grow during one run (0 = no
	// heap limit). The heap is shared with concurrent requests, so treat it as
	// a guard against runaway scripts, not exact accounting.
	Timeout   string `bcl:"timeout"`
	MaxSteps  int64  `bcl:"max_steps"`
	MaxHeapMB int64  `bcl:"max_heap_mb"`
}
