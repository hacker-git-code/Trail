package ledger

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ErrNotUndoable means the entry changed no files (an external action).
var ErrNotUndoable = errors.New("this entry changed no files, so there is nothing to put back")

// Conflict is a file that changed after the entry being undone.
type Conflict struct {
	Path   string
	Reason string
}

// Undo reverses target by restoring each file to its "before" state, then
// records the reversal as a new entry. Nothing in the ledger is rewritten.
//
// If a file no longer matches target's "after" state, Undo stops and returns
// the conflicts, unless force is set. With dryRun it only reports the plan.
func (s *Store) Undo(target *Entry, actor Actor, force, dryRun bool) (*Entry, []Change, []Conflict, error) {
	if len(target.Body.Changes) == 0 {
		return nil, nil, nil, ErrNotUndoable
	}
	unlock, err := s.lock()
	if err != nil {
		return nil, nil, nil, err
	}
	defer unlock()

	var conflicts []Conflict
	var plan []Change
	for _, c := range target.Body.Changes {
		if c.Before != "" && !s.HasBlob(c.Before) {
			return nil, nil, nil, errors.New("the saved version of " + c.Path + " is missing from the object store")
		}
		// StateOf also saves the current version, so this undo can itself be undone.
		cur, err := s.StateOf(c.Path)
		if err != nil {
			conflicts = append(conflicts, Conflict{c.Path, err.Error()})
			continue
		}
		if cur.Hash != c.After {
			reason := "was modified since"
			switch {
			case cur.Hash == "":
				reason = "was deleted since"
			case c.After == "":
				reason = "exists again since"
			}
			conflicts = append(conflicts, Conflict{c.Path, reason})
		}
		inv, changed := s.makeChange(c.Path, cur, FileState{Hash: c.Before, Mode: c.BeforeMode})
		if changed {
			if inv.AfterMode == 0 && inv.After != "" {
				inv.AfterMode = cur.Mode
			}
			plan = append(plan, inv)
		}
	}
	if len(conflicts) > 0 && !force {
		return nil, nil, conflicts, nil
	}
	if dryRun {
		return nil, plan, conflicts, nil
	}
	for _, c := range plan {
		if err := s.restore(c, target.Body.Root); err != nil {
			return nil, nil, nil, err
		}
	}
	if len(plan) == 0 {
		return nil, nil, nil, errors.New("these files are already in their earlier state")
	}
	e, err := s.appendLocked(Body{
		Actor: actor,
		Action: Action{
			Type:    "undo",
			Ref:     target.ID,
			Summary: "undid " + target.Short() + " · " + target.Body.Action.Summary,
		},
		Root:    target.Body.Root,
		Changes: plan,
	})
	return e, plan, conflicts, err
}

// restore puts one file into the change's "after" state. When it removes a
// file, it also removes folders left empty by that, up to root.
func (s *Store) restore(c Change, root string) error {
	if c.After == "" {
		if err := os.Remove(c.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		for d := filepath.Dir(c.Path); root != "" && d != root && strings.HasPrefix(d, root+string(filepath.Separator)); d = filepath.Dir(d) {
			if os.Remove(d) != nil { // fails unless empty, which is the point
				break
			}
		}
		return nil
	}
	data, err := s.GetBlob(c.After)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.Path), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(c.AfterMode)
	if mode == 0 {
		mode = 0o644
	}
	return writeFileAtomic(c.Path, data, mode)
}
