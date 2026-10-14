package ir

import (
	"encoding/hex"
	"fmt"
	"strings"
)

// formatTypeSignature formats a FuncType into a human-readable signature string,
// for example: "(i32, i32) -> (i32)".
func formatTypeSignature(ft FuncType) string {
	paramStrs := make([]string, len(ft.Params))
	for i, p := range ft.Params {
		paramStrs[i] = p.String()
	}

	resultStrs := make([]string, len(ft.Results))
	for i, r := range ft.Results {
		resultStrs[i] = r.String()
	}

	return fmt.Sprintf("func(%s) -> (%s)", strings.Join(paramStrs, ", "), strings.Join(resultStrs, ", "))
}

// Text generates a structured, human-readable textual representation of an Aegis IR module,
// suitable for debugging, inspection, logging, and compiler test assertions.
func Text(m *Module) string {
	if m == nil {
		return "<nil module>\n"
	}

	var sb strings.Builder

	sb.WriteString("module {\n")
	fmt.Fprintf(&sb, "  version: %d\n", m.Version)

	var zeroBuildID [32]byte
	if m.BuildID != zeroBuildID {
		fmt.Fprintf(&sb, "  build_id: %s\n", hex.EncodeToString(m.BuildID[:]))
	}

	if m.Metadata.CompilerVersion != "" || m.Metadata.TargetPlatform != "" || m.Metadata.BuildTime != 0 {
		sb.WriteString("  metadata {\n")
		if m.Metadata.CompilerVersion != "" {
			fmt.Fprintf(&sb, "    compiler: %q\n", m.Metadata.CompilerVersion)
		}
		if m.Metadata.TargetPlatform != "" {
			fmt.Fprintf(&sb, "    platform: %q\n", m.Metadata.TargetPlatform)
		}
		if m.Metadata.BuildTime != 0 {
			fmt.Fprintf(&sb, "    build_time: %d\n", m.Metadata.BuildTime)
		}
		sb.WriteString("  }\n")
	}

	// Types
	if len(m.Types) > 0 {
		sb.WriteString("\n  // Types\n")
		for i, ft := range m.Types {
			fmt.Fprintf(&sb, "  type %d: %s\n", i, formatTypeSignature(ft))
		}
	}

	// Imports
	if len(m.Imports) > 0 {
		sb.WriteString("\n  // Imports\n")
		for i, imp := range m.Imports {
			sig := "unknown"
			if int(imp.Type) < len(m.Types) {
				sig = formatTypeSignature(m.Types[imp.Type])
			}
			fmt.Fprintf(&sb, "  import %d: %q.%q (type %d %s)\n", i, imp.Module, imp.Name, imp.Type, sig)
		}
	}

	// Memory
	if m.Memory.InitialPages > 0 || m.Memory.MaxPages > 0 {
		sb.WriteString("\n  // Memory\n")
		fmt.Fprintf(&sb, "  memory: initial=%d pages, max=%d pages\n", m.Memory.InitialPages, m.Memory.MaxPages)
	}

	// Data
	if len(m.Data) > 0 {
		sb.WriteString("\n  // Data Segments\n")
		for i, seg := range m.Data {
			dataPreview := fmt.Sprintf("%q", string(seg.Data))
			if len(seg.Data) > 32 {
				dataPreview = fmt.Sprintf("%q... (%d bytes)", string(seg.Data[:32]), len(seg.Data))
			}
			fmt.Fprintf(&sb, "  data %d: offset=0x%04X, bytes=%d, preview=%s\n", i, seg.Offset, len(seg.Data), dataPreview)
		}
	}

	// Functions
	if len(m.Functions) > 0 {
		sb.WriteString("\n  // Functions\n")
		for i, fn := range m.Functions {
			name := fn.Name
			if name == "" {
				name = fmt.Sprintf("func_%d", i)
			}

			sig := "unknown"
			if int(fn.TypeIndex) < len(m.Types) {
				sig = formatTypeSignature(m.Types[fn.TypeIndex])
			}

			fmt.Fprintf(&sb, "  func %d $%s (type %d %s) [conf: %s",
				i, name, fn.TypeIndex, sig, fn.Attributes.Confidentiality)
			if fn.Attributes.MustNotInline {
				sb.WriteString(", noinline")
			}
			if fn.Attributes.Diversify {
				sb.WriteString(", diversify")
			}
			sb.WriteString("] {\n")

			// Locals
			if len(fn.Locals) > 0 {
				localStrs := make([]string, len(fn.Locals))
				for lIdx, lType := range fn.Locals {
					localStrs[lIdx] = lType.String()
				}
				fmt.Fprintf(&sb, "    locals: [%s]\n", strings.Join(localStrs, ", "))
			}

			// Instructions
			for instIdx, inst := range fn.Body {
				opStr := inst.Op.String()
				opCount := inst.Op.OperandCount()

				switch {
				case opCount == 1:
					switch inst.Op {
					case OpCall:
						targetName := ""
						if inst.Operands[0] < uint64(len(m.Functions)) && m.Functions[inst.Operands[0]].Name != "" {
							targetName = fmt.Sprintf(" ($%s)", m.Functions[inst.Operands[0]].Name)
						}
						fmt.Fprintf(&sb, "    %-14s %d%s\n", opStr, inst.Operands[0], targetName)
					case OpCallImport:
						targetName := ""
						if inst.Operands[0] < uint64(len(m.Imports)) {
							targetName = fmt.Sprintf(" ($%s.%s)", m.Imports[inst.Operands[0]].Module, m.Imports[inst.Operands[0]].Name)
						}
						fmt.Fprintf(&sb, "    %-14s %d%s\n", opStr, inst.Operands[0], targetName)
					default:
						fmt.Fprintf(&sb, "    %-14s %d\n", opStr, inst.Operands[0])
					}
				case opCount == 2:
					fmt.Fprintf(&sb, "    %-14s %d %d\n", opStr, inst.Operands[0], inst.Operands[1])
				case opCount >= 3:
					fmt.Fprintf(&sb, "    %-14s %d %d %d\n", opStr, inst.Operands[0], inst.Operands[1], inst.Operands[2])
				default:
					// Check if extra operands are present even when OperandCount is 0
					if inst.Operands[0] != 0 || inst.Operands[1] != 0 || inst.Operands[2] != 0 {
						fmt.Fprintf(&sb, "    %-14s %d %d %d\n", opStr, inst.Operands[0], inst.Operands[1], inst.Operands[2])
					} else {
						fmt.Fprintf(&sb, "    %s\n", opStr)
					}
				}
				_ = instIdx
			}
			sb.WriteString("  }\n")
		}
	}

	// Exports
	if len(m.Exports) > 0 {
		sb.WriteString("\n  // Exports\n")
		for i, exp := range m.Exports {
			target := fmt.Sprintf("func %d", exp.FuncIndex)
			if exp.FuncIndex < uint32(len(m.Functions)) && m.Functions[exp.FuncIndex].Name != "" {
				target = fmt.Sprintf("func %d ($%s)", exp.FuncIndex, m.Functions[exp.FuncIndex].Name)
			}
			fmt.Fprintf(&sb, "  export %d: %q -> %s\n", i, exp.Name, target)
		}
	}

	sb.WriteString("}\n")
	return sb.String()
}
