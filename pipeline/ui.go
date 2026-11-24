package pipeline

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// UISpec is how a workflow looks and how its screens are laid out. Everything
// is optional: a workflow with no ui block gets the host's basic layout. The
// host renders what is declared here, so a workflow's appearance lives in its
// own definition.
type UISpec struct {
	// Brand replaces the product name in the host's sidebar while the
	// workflow's screens are open.
	Brand string `bcl:"brand" json:"brand,omitempty"`
	// Accent is the workflow's one brand colour, a hex value like "#0f766e".
	// The host derives its tints and text colour from it; it never becomes a
	// gradient.
	Accent string `bcl:"accent" json:"accent,omitempty"`
	// Density is comfortable (default) or compact.
	Density string `bcl:"density,ident" json:"density,omitempty"`
	// Overview lays out the case overview; Task the screen where a stage is
	// worked. A stage's own ui block overrides Task for that stage.
	Overview *LayoutSpec `bcl:"overview" json:"overview,omitempty"`
	Task     *LayoutSpec `bcl:"task" json:"task,omitempty"`
}

// LayoutSpec arranges the panels of one screen.
type LayoutSpec struct {
	// Mode is console (columns side by side), split (a main column and a
	// rail), or stacked (one column). Without Columns each mode uses the
	// host's default arrangement of panels.
	Mode string `bcl:"mode,ident" json:"mode,omitempty"`
	// Flow shows the strip of stages above the overview: show (default) or hide.
	Flow string `bcl:"flow,ident" json:"flow,omitempty"`
	// Columns place panels explicitly, left to right (top to bottom when
	// stacked). A panel left out of every column is not shown.
	Columns []UIColumn `bcl:"column,block" json:"columns,omitempty"`
	// Panels adjust individual panels: a title, hidden, or collapsed.
	Panels []UIPanel `bcl:"panel,block" json:"panels,omitempty"`
}

// UIColumn is one column of a layout.
type UIColumn struct {
	Name string `bcl:",id" json:"name"`
	// Width is a CSS length: "320px", "30%", "24rem" or a share such as "1fr".
	Width string `bcl:"width" json:"width,omitempty"`
	// Panels lists the panels in the column, in order.
	Panels []string `bcl:"panels" json:"panels"`
}

// UIPanel adjusts one panel.
type UIPanel struct {
	ID        string `bcl:",id" json:"id"`
	Title     string `bcl:"title" json:"title,omitempty"`
	Hidden    bool   `bcl:"hidden" json:"hidden,omitempty"`
	Collapsed bool   `bcl:"collapsed" json:"collapsed,omitempty"`
}

// The panels each screen offers.
var (
	OverviewPanels = []string{"case", "stage", "actions", "stand", "next", "certificate", "party", "entries", "summary", "log", "missed"}
	TaskPanels     = []string{"form", "checks", "stage", "case"}
)

var (
	hexColour = regexp.MustCompile(`^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)
	cssLength = regexp.MustCompile(`^(auto|\d+(\.\d+)?(px|%|rem|em|fr))$`)
)

// UIView is a workflow's resolved look for one screen, as the host receives it.
type UIView struct {
	Brand   string      `json:"brand,omitempty"`
	Accent  string      `json:"accent,omitempty"`
	Density string      `json:"density,omitempty"`
	Layout  *LayoutSpec `json:"layout,omitempty"`
}

// uiFor resolves the look for a surface ("overview" or "task"); a stage's own
// layout replaces the workflow's for the task screen.
func (e *Engine) uiFor(surface string, st *Stage) *UIView {
	ui := e.C.Def.UI
	var layout *LayoutSpec
	if ui != nil {
		if surface == "overview" {
			layout = ui.Overview
		} else {
			layout = ui.Task
		}
	}
	if surface == "task" && st != nil && st.UI != nil {
		layout = st.UI
	}
	if ui == nil && layout == nil {
		return nil
	}
	v := &UIView{Layout: layout}
	if ui != nil {
		v.Brand, v.Accent, v.Density = ui.Brand, ui.Accent, ui.Density
	}
	return v
}

func checkLayout(where string, l *LayoutSpec, panels []string) error {
	if l == nil {
		return nil
	}
	if !oneOf(l.Mode, "", "console", "split", "stacked") {
		return fmt.Errorf("%s: mode %q is not console, split or stacked", where, l.Mode)
	}
	if !oneOf(l.Flow, "", "show", "hide") {
		return fmt.Errorf("%s: flow %q is not show or hide", where, l.Flow)
	}
	seen := map[string]bool{}
	names := map[string]bool{}
	for _, c := range l.Columns {
		if strings.TrimSpace(c.Name) == "" || names[c.Name] {
			return fmt.Errorf("%s: columns need distinct names", where)
		}
		names[c.Name] = true
		if c.Width != "" && !cssLength.MatchString(c.Width) {
			return fmt.Errorf("%s: column %q width %q is not a length like 320px, 30%% or 1fr", where, c.Name, c.Width)
		}
		if len(c.Panels) == 0 {
			return fmt.Errorf("%s: column %q lists no panels", where, c.Name)
		}
		for _, p := range c.Panels {
			if !slices.Contains(panels, p) {
				return fmt.Errorf("%s: column %q names unknown panel %q (panels are %s)", where, c.Name, p, strings.Join(panels, ", "))
			}
			if seen[p] {
				return fmt.Errorf("%s: panel %q appears twice", where, p)
			}
			seen[p] = true
		}
	}
	for _, p := range l.Panels {
		if !slices.Contains(panels, p.ID) {
			return fmt.Errorf("%s: panel %q is not one of %s", where, p.ID, strings.Join(panels, ", "))
		}
	}
	return nil
}

func checkUI(def *Definition) error {
	ui := def.UI
	if ui != nil {
		if ui.Accent != "" && !hexColour.MatchString(ui.Accent) {
			return fmt.Errorf("ui: accent %q is not a hex colour like #0f766e", ui.Accent)
		}
		if !oneOf(ui.Density, "", "comfortable", "compact") {
			return fmt.Errorf("ui: density %q is not comfortable or compact", ui.Density)
		}
		if err := checkLayout("ui overview", ui.Overview, OverviewPanels); err != nil {
			return err
		}
		if err := checkLayout("ui task", ui.Task, TaskPanels); err != nil {
			return err
		}
	}
	for _, st := range def.Stages {
		if err := checkLayout("stage "+st.Name+" ui", st.UI, TaskPanels); err != nil {
			return err
		}
	}
	return nil
}
