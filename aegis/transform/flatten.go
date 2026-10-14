// Package transform provides program transformation and protection passes for Aegis IR modules.
package transform

import (
	"errors"
	"math/rand"
	"time"

	"github.com/oarkflow/ref/aegis/ir"
)

// ControlFlowFlatten implements Chenxi Wang's control flow flattening transformation.
// It converts hierarchical control flow (loops, if-else branches, sequential blocks)
// into a flat state-machine dispatcher governed by a mutable state variable.
type ControlFlowFlatten struct {
	seed int64
}

// NewControlFlowFlatten constructs a new ControlFlowFlatten pass with an optional random seed.
func NewControlFlowFlatten(seed ...int64) *ControlFlowFlatten {
	s := time.Now().UnixNano()
	if len(seed) > 0 && seed[0] != 0 {
		s = seed[0]
	}
	return &ControlFlowFlatten{seed: s}
}

// Name returns the identifier of this transformation pass.
func (c *ControlFlowFlatten) Name() string {
	return "ControlFlowFlatten"
}

// BasicBlock represents an atomic sequence of straight-line instructions and its state transitions.
type BasicBlock struct {
	ID       uint32
	Body     []ir.Instruction
	IsReturn bool

	// Unconditional jump transition: NextID
	NextID uint32

	// Conditional branch transition
	IsCond   bool
	CondBody []ir.Instruction
	TrueID   uint32
	FalseID  uint32
}

// idGenerator generates unique non-zero random state IDs.
type idGenerator struct {
	rng  *rand.Rand
	used map[uint32]bool
}

func newIDGenerator(rng *rand.Rand) *idGenerator {
	return &idGenerator{
		rng:  rng,
		used: make(map[uint32]bool),
	}
}

func (g *idGenerator) nextID() uint32 {
	for {
		id := (g.rng.Uint32() & 0x7FFFFFFF) + 1
		if !g.used[id] {
			g.used[id] = true
			return id
		}
	}
}

// astNode represents a parsed structured control construct.
type astNode interface {
	isASTNode()
}

type stmtNode struct {
	insts []ir.Instruction
}

func (s *stmtNode) isASTNode() {}

type returnNode struct {
	insts []ir.Instruction
}

func (r *returnNode) isASTNode() {}

type ifNode struct {
	cond []ir.Instruction
	then []astNode
	el   []astNode
}

func (i *ifNode) isASTNode() {}

type forNode struct {
	cond []ir.Instruction
	body []astNode
}

func (f *forNode) isASTNode() {}

// computeStackDelta calculates stack pop and push counts for an instruction.
func computeStackDelta(inst ir.Instruction, types []ir.FuncType, imports []ir.Import) (int, int) {
	switch inst.Op {
	case ir.OpNop, ir.OpBlock, ir.OpLoop, ir.OpElse, ir.OpEnd, ir.OpBr, ir.OpTrap:
		return 0, 0

	case ir.OpIf, ir.OpBrIf, ir.OpBrTable:
		return 1, 0

	case ir.OpLocalGet, ir.OpI32Const, ir.OpI64Const, ir.OpF32Const, ir.OpF64Const, ir.OpMemorySize:
		return 0, 1

	case ir.OpLocalSet:
		return 1, 0

	case ir.OpLocalTee, ir.OpLoad32, ir.OpLoad64, ir.OpLoadF32, ir.OpLoadF64, ir.OpMemoryGrow:
		return 1, 1

	case ir.OpStore32, ir.OpStore64, ir.OpStoreF32, ir.OpStoreF64:
		return 2, 0

	case ir.OpI32Add, ir.OpI32Sub, ir.OpI32Mul, ir.OpI32DivS, ir.OpI32DivU,
		ir.OpI32RemS, ir.OpI32RemU, ir.OpI32And, ir.OpI32Or, ir.OpI32Xor,
		ir.OpI32Shl, ir.OpI32ShrS, ir.OpI32ShrU,
		ir.OpI32Eq, ir.OpI32Ne, ir.OpI32LtS, ir.OpI32LtU, ir.OpI32GtS, ir.OpI32GtU,
		ir.OpI32LeS, ir.OpI32LeU, ir.OpI32GeS, ir.OpI32GeU,
		ir.OpI64Add, ir.OpI64Sub, ir.OpI64Mul, ir.OpI64DivS, ir.OpI64DivU,
		ir.OpI64RemS, ir.OpI64RemU, ir.OpI64And, ir.OpI64Or, ir.OpI64Xor,
		ir.OpI64Shl, ir.OpI64ShrS, ir.OpI64ShrU,
		ir.OpI64Eq, ir.OpI64Ne, ir.OpI64LtS, ir.OpI64LtU, ir.OpI64GtS, ir.OpI64GtU,
		ir.OpI64LeS, ir.OpI64LeU, ir.OpI64GeS, ir.OpI64GeU,
		ir.OpF64Add, ir.OpF64Sub, ir.OpF64Mul, ir.OpF64Div,
		ir.OpF64Eq, ir.OpF64Ne, ir.OpF64Lt, ir.OpF64Gt, ir.OpF64Le, ir.OpF64Ge:
		return 2, 1

	case ir.OpI32Eqz, ir.OpI64Eqz, ir.OpI32WrapI64, ir.OpI64ExtendI32S, ir.OpI64ExtendI32U,
		ir.OpF64ConvertI32S, ir.OpF64ConvertI64S, ir.OpI32TruncF64S:
		return 1, 1

	case ir.OpCall:
		// Target func signature
		calleeIdx := inst.Operands[0]
		_ = calleeIdx
		return 0, 0

	case ir.OpCallImport:
		impIdx := int(inst.Operands[0])
		if impIdx < len(imports) {
			typeIdx := imports[impIdx].Type
			if int(typeIdx) < len(types) {
				return len(types[typeIdx].Params), len(types[typeIdx].Results)
			}
		}
		return 0, 0

	case ir.OpReturn:
		return 0, 0

	default:
		return 0, 0
	}
}

// findMatchingEnd finds the index of the matching OpEnd for a block starting at startPC.
func findMatchingEnd(insts []ir.Instruction, startPC int) (endPC int, elsePC int) {
	elsePC = -1
	depth := 0
	for i := startPC; i < len(insts); i++ {
		switch insts[i].Op {
		case ir.OpBlock, ir.OpLoop, ir.OpIf:
			depth++
		case ir.OpElse:
			if depth == 1 {
				elsePC = i
			}
		case ir.OpEnd:
			depth--
			if depth == 0 {
				return i, elsePC
			}
		}
	}
	return len(insts) - 1, elsePC
}

// parseASTNodes parses an instruction slice into structured AST nodes.
func parseASTNodes(insts []ir.Instruction, types []ir.FuncType, imports []ir.Import) []astNode {
	var nodes []astNode
	pc := 0

	for pc < len(insts) {
		// 1. Detect for loop pattern: OpBlock followed by OpLoop
		if insts[pc].Op == ir.OpBlock && pc+1 < len(insts) && insts[pc+1].Op == ir.OpLoop {
			blockEnd, _ := findMatchingEnd(insts, pc)
			loopEnd, _ := findMatchingEnd(insts, pc+1)

			// Look for condition inside loop: instructions up to OpI32Eqz + OpBrIf 1
			innerStart := pc + 2
			innerEnd := loopEnd
			// Exclude final OpBr 0 if present
			if innerEnd > innerStart && insts[innerEnd-1].Op == ir.OpBr && insts[innerEnd-1].Operands[0] == 0 {
				innerEnd--
			}

			// Scan for condition ending in OpI32Eqz + OpBrIf 1
			var (
				condInsts []ir.Instruction
				bodyStart = innerStart
			)
			for i := innerStart; i < innerEnd-1; i++ {
				if insts[i].Op == ir.OpI32Eqz && insts[i+1].Op == ir.OpBrIf && insts[i+1].Operands[0] == 1 {
					condInsts = insts[innerStart:i]
					bodyStart = i + 2
					break
				}
			}

			bodyNodes := parseASTNodes(insts[bodyStart:innerEnd], types, imports)
			nodes = append(nodes, &forNode{
				cond: condInsts,
				body: bodyNodes,
			})
			pc = blockEnd + 1
			continue
		}

		// 2. Detect if pattern: instructions ending at OpIf
		if insts[pc].Op == ir.OpIf {
			// OpIf without preceding condition in this chunk
			ifEnd, elsePC := findMatchingEnd(insts, pc)
			var thenInsts, elseInsts []ir.Instruction
			if elsePC != -1 {
				thenInsts = insts[pc+1 : elsePC]
				elseInsts = insts[elsePC+1 : ifEnd]
			} else {
				thenInsts = insts[pc+1 : ifEnd]
			}
			thenNodes := parseASTNodes(thenInsts, types, imports)
			var elseNodes []astNode
			if len(elseInsts) > 0 {
				elseNodes = parseASTNodes(elseInsts, types, imports)
			}
			nodes = append(nodes, &ifNode{
				cond: nil,
				then: thenNodes,
				el:   elseNodes,
			})
			pc = ifEnd + 1
			continue
		}

		// Look ahead to check if next construct is an OpIf with condition
		// Find next control opcode or stack-depth 0 boundary
		startPC := pc
		var (
			hasIf      = false
			ifLocation = -1
		)

		for i := pc; i < len(insts); i++ {
			if insts[i].Op == ir.OpIf {
				hasIf = true
				ifLocation = i
				break
			}
			if insts[i].Op == ir.OpBlock || insts[i].Op == ir.OpReturn {
				break
			}
		}

		if hasIf && ifLocation > startPC {
			// Instructions between startPC and ifLocation evaluate the if condition
			condInsts := insts[startPC:ifLocation]
			ifEnd, elsePC := findMatchingEnd(insts, ifLocation)
			var thenInsts, elseInsts []ir.Instruction
			if elsePC != -1 {
				thenInsts = insts[ifLocation+1 : elsePC]
				elseInsts = insts[elsePC+1 : ifEnd]
			} else {
				thenInsts = insts[ifLocation+1 : ifEnd]
			}
			thenNodes := parseASTNodes(thenInsts, types, imports)
			var elseNodes []astNode
			if len(elseInsts) > 0 {
				elseNodes = parseASTNodes(elseInsts, types, imports)
			}
			nodes = append(nodes, &ifNode{
				cond: condInsts,
				then: thenNodes,
				el:   elseNodes,
			})
			pc = ifEnd + 1
			continue
		}

		// 3. Scan straight-line statement or return
		endPC := pc
		for endPC < len(insts) {
			op := insts[endPC].Op
			if op == ir.OpReturn {
				endPC++
				nodes = append(nodes, &returnNode{
					insts: insts[startPC:endPC],
				})
				pc = endPC
				break
			}
			if op == ir.OpLocalSet || op == ir.OpStore32 || op == ir.OpStore64 || op == ir.OpNop {
				endPC++
				nodes = append(nodes, &stmtNode{
					insts: insts[startPC:endPC],
				})
				pc = endPC
				break
			}
			if op == ir.OpBlock || op == ir.OpIf {
				// Stop before structured control flow
				if endPC > startPC {
					nodes = append(nodes, &stmtNode{
						insts: insts[startPC:endPC],
					})
					pc = endPC
				} else {
					// Fallback single instruction
					endPC++
					nodes = append(nodes, &stmtNode{
						insts: insts[startPC:endPC],
					})
					pc = endPC
				}
				break
			}
			endPC++
			if endPC == len(insts) {
				nodes = append(nodes, &stmtNode{
					insts: insts[startPC:endPC],
				})
				pc = endPC
				break
			}
		}
	}

	return nodes
}

// lowerASTNodes lowers a slice of AST nodes into flat basic blocks.
func lowerASTNodes(nodes []astNode, nextTarget uint32, gen *idGenerator) (uint32, []*BasicBlock) {
	if len(nodes) == 0 {
		return nextTarget, nil
	}

	currentTarget := nextTarget
	var allBlocks []*BasicBlock

	// Lower in reverse so each node knows its succeeding state ID
	for i := len(nodes) - 1; i >= 0; i-- {
		entryID, blocks := lowerSingleNode(nodes[i], currentTarget, gen)
		allBlocks = append(blocks, allBlocks...)
		currentTarget = entryID
	}

	return currentTarget, allBlocks
}

// lowerSingleNode translates an individual AST node into one or more basic blocks.
func lowerSingleNode(node astNode, nextTarget uint32, gen *idGenerator) (uint32, []*BasicBlock) {
	switch n := node.(type) {
	case *returnNode:
		block := &BasicBlock{
			ID:       gen.nextID(),
			Body:     n.insts,
			IsReturn: true,
		}
		return block.ID, []*BasicBlock{block}

	case *stmtNode:
		block := &BasicBlock{
			ID:     gen.nextID(),
			Body:   n.insts,
			NextID: nextTarget,
		}
		return block.ID, []*BasicBlock{block}

	case *ifNode:
		thenEntry, thenBlocks := lowerASTNodes(n.then, nextTarget, gen)
		elseEntry := nextTarget
		var elseBlocks []*BasicBlock
		if len(n.el) > 0 {
			elseEntry, elseBlocks = lowerASTNodes(n.el, nextTarget, gen)
		}

		condBlock := &BasicBlock{
			ID:       gen.nextID(),
			IsCond:   true,
			CondBody: n.cond,
			TrueID:   thenEntry,
			FalseID:  elseEntry,
		}

		resultBlocks := make([]*BasicBlock, 0, 1+len(thenBlocks)+len(elseBlocks))
		resultBlocks = append(resultBlocks, condBlock)
		resultBlocks = append(resultBlocks, thenBlocks...)
		resultBlocks = append(resultBlocks, elseBlocks...)
		return condBlock.ID, resultBlocks

	case *forNode:
		condID := gen.nextID()
		bodyEntry, bodyBlocks := lowerASTNodes(n.body, condID, gen)

		condBlock := &BasicBlock{
			ID:       condID,
			IsCond:   true,
			CondBody: n.cond,
			TrueID:   bodyEntry,
			FalseID:  nextTarget,
		}

		resultBlocks := make([]*BasicBlock, 0, 1+len(bodyBlocks))
		resultBlocks = append(resultBlocks, condBlock)
		resultBlocks = append(resultBlocks, bodyBlocks...)
		return condBlock.ID, resultBlocks

	default:
		block := &BasicBlock{
			ID:     gen.nextID(),
			NextID: nextTarget,
		}
		return block.ID, []*BasicBlock{block}
	}
}

// Transform flattens all internal function bodies into dispatcher loops.
func (c *ControlFlowFlatten) Transform(m *ir.Module) (*ir.Module, Metrics, error) {
	if m == nil {
		return nil, Metrics{}, errors.New("transform: module is nil")
	}

	rng := rand.New(rand.NewSource(c.seed))
	metrics := Metrics{PassName: c.Name()}

	typeCount := len(m.Types)

	for funcIdx := range m.Functions {
		fn := &m.Functions[funcIdx]
		// Skip empty or single-instruction functions
		if len(fn.Body) <= 1 {
			continue
		}

		var paramCount int
		if int(fn.TypeIndex) < typeCount {
			paramCount = len(m.Types[fn.TypeIndex].Params)
		}

		gen := newIDGenerator(rng)
		exitID := gen.nextID()

		// 1. Parse function body into AST nodes
		nodes := parseASTNodes(fn.Body, m.Types, m.Imports)
		if len(nodes) == 0 {
			continue
		}

		// 2. Lower AST nodes into flat basic blocks
		entryID, blocks := lowerASTNodes(nodes, exitID, gen)
		if len(blocks) == 0 {
			continue
		}

		// 3. Allocate state variable in fn.Locals
		stateLocal := uint32(paramCount + len(fn.Locals))
		fn.Locals = append(fn.Locals, ir.I32)

		// 4. Shuffle basic blocks order in the dispatcher switch
		shuffledBlocks := make([]*BasicBlock, len(blocks))
		perm := rng.Perm(len(blocks))
		for i, p := range perm {
			shuffledBlocks[i] = blocks[p]
		}

		// 5. Construct flattened dispatcher bytecode
		var newBody []ir.Instruction

		// Initialize state variable = entryID
		newBody = append(newBody,
			ir.Instruction{Op: ir.OpI32Const, Operands: [3]uint64{uint64(entryID), 0, 0}},
			ir.Instruction{Op: ir.OpLocalSet, Operands: [3]uint64{uint64(stateLocal), 0, 0}},
		)

		// Dispatcher loop: OpBlock wrapping OpLoop
		newBody = append(newBody,
			ir.Instruction{Op: ir.OpBlock},
			ir.Instruction{Op: ir.OpLoop},
		)

		// Switch cases for each basic block
		for _, b := range shuffledBlocks {
			// Check if state == b.ID
			newBody = append(newBody,
				ir.Instruction{Op: ir.OpLocalGet, Operands: [3]uint64{uint64(stateLocal), 0, 0}},
				ir.Instruction{Op: ir.OpI32Const, Operands: [3]uint64{uint64(b.ID), 0, 0}},
				ir.Instruction{Op: ir.OpI32Eq},
				ir.Instruction{Op: ir.OpIf},
			)

			// Block body instructions
			newBody = append(newBody, b.Body...)

			// Transition logic
			if b.IsReturn {
				// If body doesn't end in return, emit OpReturn
				if len(b.Body) == 0 || b.Body[len(b.Body)-1].Op != ir.OpReturn {
					newBody = append(newBody, ir.Instruction{Op: ir.OpReturn})
				}
			} else if b.IsCond {
				// Evaluate condition
				newBody = append(newBody, b.CondBody...)
				newBody = append(newBody,
					ir.Instruction{Op: ir.OpIf},
					ir.Instruction{Op: ir.OpI32Const, Operands: [3]uint64{uint64(b.TrueID), 0, 0}},
					ir.Instruction{Op: ir.OpLocalSet, Operands: [3]uint64{uint64(stateLocal), 0, 0}},
					ir.Instruction{Op: ir.OpElse},
					ir.Instruction{Op: ir.OpI32Const, Operands: [3]uint64{uint64(b.FalseID), 0, 0}},
					ir.Instruction{Op: ir.OpLocalSet, Operands: [3]uint64{uint64(stateLocal), 0, 0}},
					ir.Instruction{Op: ir.OpEnd},
					// Branch to start of OpLoop (depth 1 from inside case if)
					ir.Instruction{Op: ir.OpBr, Operands: [3]uint64{1, 0, 0}},
				)
			} else {
				// Unconditional transition to NextID
				newBody = append(newBody,
					ir.Instruction{Op: ir.OpI32Const, Operands: [3]uint64{uint64(b.NextID), 0, 0}},
					ir.Instruction{Op: ir.OpLocalSet, Operands: [3]uint64{uint64(stateLocal), 0, 0}},
					// Branch to start of OpLoop (depth 1 from inside case if)
					ir.Instruction{Op: ir.OpBr, Operands: [3]uint64{1, 0, 0}},
				)
			}

			// End of case if
			newBody = append(newBody, ir.Instruction{Op: ir.OpEnd})
		}

		// End of loop iteration: continue loop
		newBody = append(newBody,
			ir.Instruction{Op: ir.OpBr, Operands: [3]uint64{0, 0, 0}},
			ir.Instruction{Op: ir.OpEnd}, // end of OpLoop
			ir.Instruction{Op: ir.OpEnd}, // end of OpBlock
		)

		// Catch-all exit return for safety
		newBody = append(newBody, ir.Instruction{Op: ir.OpReturn})

		metrics.InstructionsRemoved += len(fn.Body)
		metrics.InstructionsAdded += len(newBody)
		fn.Body = newBody
		metrics.FunctionsModified++
	}

	return m, metrics, nil
}
