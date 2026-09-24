package graph

type Simulation struct {
	Nodes          int
	Stages         int
	MaxParallelism int
	CriticalPath   int
	Widths         []int
}

func Simulate(plan *Plan) Simulation {
	if plan == nil {
		return Simulation{}
	}
	if len(plan.Nodes) == 0 {
		return Simulation{}
	}
	remaining := make([]int32, len(plan.InitialDeps))
	copy(remaining, plan.InitialDeps)
	widths := make([]int, 0, len(plan.Nodes))
	critical := make([]int, len(plan.Nodes))
	maxWidth := 0
	for len(remaining) > 0 {
		ready := make([]NodeID, 0)
		for id, count := range remaining {
			if count == 0 {
				ready = append(ready, NodeID(id))
			}
		}
		if len(ready) == 0 {
			break
		}
		widths = append(widths, len(ready))
		if len(ready) > maxWidth {
			maxWidth = len(ready)
		}
		for _, id := range ready {
			remaining[id] = -1
		}
		for _, id := range ready {
			for _, downstream := range plan.Adjacency[id] {
				if remaining[downstream] < 0 {
					continue
				}
				remaining[downstream]--
				if critical[downstream] < critical[id]+1 {
					critical[downstream] = critical[id] + 1
				}
			}
		}
	}
	criticalPath := 0
	for _, value := range critical {
		if value > criticalPath {
			criticalPath = value
		}
	}
	return Simulation{Nodes: len(plan.Nodes), Stages: len(widths), MaxParallelism: maxWidth, CriticalPath: criticalPath + 1, Widths: widths}
}

func (p *Plan) Simulation() Simulation {
	return Simulate(p)
}
