package main

import (
	"github.com/gotk3/gotk3/gtk"
)

// ---------- tree model helpers ----------
//
// gotk3 splits ListStore and TreeModel into separate types; these helpers
// bridge the gaps (iteration, selection, value reading).

// treeModelOf returns the *TreeModel view of a treeview's model.
// NB: gtk.TreeView.GetModel() returns the CONCRETE wrapper — *gtk.ListStore
// for list-backed trees — so a bare .(*gtk.TreeModel) assertion fails and
// silently breaks selection reading and drop-target resolution.
func treeModelOf(tv *gtk.TreeView) (*gtk.TreeModel, bool) {
	m, err := tv.GetModel()
	if err != nil {
		return nil, false
	}
	return anyTreeModel(m)
}

// anyTreeModel converts any gotk3 tree-model wrapper to *gtk.TreeModel.
func anyTreeModel(m interface{}) (*gtk.TreeModel, bool) {
	switch mm := m.(type) {
	case *gtk.TreeModel:
		return mm, true
	case *gtk.ListStore:
		return mm.ToTreeModel(), true
	case *gtk.TreeStore:
		return mm.ToTreeModel(), true
	}
	return nil, false
}

// selIter returns (model, iter, ok) for the currently selected row.
func selIter(tv *gtk.TreeView) (*gtk.TreeModel, *gtk.TreeIter, bool) {
	sel, err := tv.GetSelection()
	if err != nil {
		return nil, nil, false
	}
	m, iter, ok := sel.GetSelected()
	if !ok {
		return nil, nil, false
	}
	tm, ok2 := m.(*gtk.TreeModel)
	if !ok2 {
		return nil, nil, false
	}
	return tm, iter, true
}

// iterGetString reads a string cell from a model.
func iterGetString(m *gtk.TreeModel, iter *gtk.TreeIter, col int) string {
	val, err := m.GetValue(iter, col)
	if err != nil {
		return ""
	}
	s, err := val.GetString()
	if err != nil {
		return ""
	}
	return s
}

// selectedStrings returns strings of col for all selected rows.
func selectedStrings(tv *gtk.TreeView, col int) []string {
	sel, err := tv.GetSelection()
	if err != nil {
		return nil
	}
	m, ok := treeModelOf(tv)
	if !ok {
		return nil
	}
	var out []string
	iter, ok2 := m.GetIterFirst()
	for ok2 {
		if sel.IterIsSelected(iter) {
			out = append(out, iterGetString(m, iter, col))
		}
		ok2 = m.IterNext(iter)
	}
	return out
}

// selectedInt reads an int from the selected row (e.g. row index column).
func selectedInt(tv *gtk.TreeView, col int) int {
	m, iter, ok := selIter(tv)
	if !ok {
		return -1
	}
	return atoiSafe(iterGetString(m, iter, col))
}

// replaceListRow writes values into the given columns of a row.
func replaceListRow(st *gtk.ListStore, iter *gtk.TreeIter, cols []int, vals []interface{}) {
	_ = st.Set(iter, cols, vals)
}
