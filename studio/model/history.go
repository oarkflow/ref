package model

import "errors"

// History is an undo/redo stack over immutable Files. Undo restores the exact
// bytes of the earlier version because it restores the earlier File.
type History struct {
	past   []*File
	future []*File
	cur    *File
}

// NewHistory starts a history at f.
func NewHistory(f *File) *History { return &History{cur: f} }

// Current is the file the history is at.
func (h *History) Current() *File { return h.cur }

// Apply runs op on the current file. On success the result becomes current and
// the redo stack is cleared; on error nothing changes.
func (h *History) Apply(op func(*File) (*File, error)) error {
	nf, err := op(h.cur)
	if err != nil {
		return err
	}
	if nf.Equal(h.cur) {
		return nil // a no-op leaves no undo step
	}
	h.past = append(h.past, h.cur)
	h.cur = nf
	h.future = nil
	return nil
}

// CanUndo and CanRedo report whether a step is available.
func (h *History) CanUndo() bool { return len(h.past) > 0 }
func (h *History) CanRedo() bool { return len(h.future) > 0 }

// Undo steps back one edit.
func (h *History) Undo() error {
	if len(h.past) == 0 {
		return errors.New("model: nothing to undo")
	}
	h.future = append(h.future, h.cur)
	h.cur = h.past[len(h.past)-1]
	h.past = h.past[:len(h.past)-1]
	return nil
}

// Redo re-applies an undone edit.
func (h *History) Redo() error {
	if len(h.future) == 0 {
		return errors.New("model: nothing to redo")
	}
	h.past = append(h.past, h.cur)
	h.cur = h.future[len(h.future)-1]
	h.future = h.future[:len(h.future)-1]
	return nil
}

// Len is the number of undoable steps.
func (h *History) Len() int { return len(h.past) }
