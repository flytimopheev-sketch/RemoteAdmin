package main

// dnd_cgo.go — thin cgo shims for GTK drag-and-drop functions that gotk3
// v0.6.1 does not wrap (gtk_drag_get_data / gtk_drag_finish).

// #cgo pkg-config: gtk+-3.0
// #include <gtk/gtk.h>
import "C"

import (
	"unsafe"

	"github.com/gotk3/gotk3/gdk"
	"github.com/gotk3/gotk3/gtk"
)

// dragGetData requests the drag data for the given target (gtk_drag_get_data).
func dragGetData(w *gtk.TreeView, ctx *gdk.DragContext, target gdk.Atom, time uint32) {
	if w == nil || ctx == nil {
		return
	}
	C.gtk_drag_get_data(
		(*C.GtkWidget)(unsafe.Pointer(w.ToWidget().Native())),
		(*C.GdkDragContext)(unsafe.Pointer(ctx.Native())),
		C.GdkAtom(unsafe.Pointer(target)),
		C.guint(time),
	)
}

// dragFinish completes a drop (gtk_drag_finish).
func dragFinish(ctx *gdk.DragContext, success, del bool, time uint32) {
	if ctx == nil {
		return
	}
	var s, d C.gboolean
	if success {
		s = C.gboolean(1)
	}
	if del {
		d = C.gboolean(1)
	}
	C.gtk_drag_finish(
		(*C.GdkDragContext)(unsafe.Pointer(ctx.Native())),
		s, d, C.guint(time),
	)
}

// treeViewDestRowAtPos resolves the drop row with
// gtk_tree_view_get_dest_row_at_pos — the function GTK's own tree DnD uses.
// gtk_tree_view_get_path_at_pos proved off-by-one-row for drag-event
// coordinates (they are bin-window relative), so raw coordinates must not be
// used for drop targeting.
func treeViewDestRowAtPos(tv *gtk.TreeView, x, y int) *gtk.TreePath {
	if tv == nil {
		return nil
	}
	var p *C.GtkTreePath
	var pos C.GtkTreeViewDropPosition
	ok := C.gtk_tree_view_get_dest_row_at_pos(
		(*C.GtkTreeView)(unsafe.Pointer(tv.ToWidget().Native())),
		C.gint(x), C.gint(y),
		&p, &pos,
	)
	if ok == 0 || p == nil {
		return nil
	}
	str := C.GoString(C.gtk_tree_path_to_string(p))
	C.gtk_tree_path_free(p)
	tp, err := gtk.TreePathNewFromString(str)
	if err != nil {
		return nil
	}
	return tp
}

// treeViewDestRowAtPosEx — как treeViewDestRowAtPos, но возвращает и позицию
// вставки (BEFORE/AFTER/INTO…), которая нужна для сортировки задач плейбука.
func treeViewDestRowAtPosEx(tv *gtk.TreeView, x, y int) (*gtk.TreePath, gtk.TreeViewDropPosition) {
	if tv == nil {
		return nil, gtk.TREE_VIEW_DROP_AFTER
	}
	var p *C.GtkTreePath
	var pos C.GtkTreeViewDropPosition
	ok := C.gtk_tree_view_get_dest_row_at_pos(
		(*C.GtkTreeView)(unsafe.Pointer(tv.ToWidget().Native())),
		C.gint(x), C.gint(y),
		&p, &pos,
	)
	if ok == 0 || p == nil {
		return nil, gtk.TREE_VIEW_DROP_AFTER
	}
	str := C.GoString(C.gtk_tree_path_to_string(p))
	C.gtk_tree_path_free(p)
	tp, err := gtk.TreePathNewFromString(str)
	if err != nil {
		return nil, gtk.TREE_VIEW_DROP_AFTER
	}
	return tp, gtk.TreeViewDropPosition(pos)
}

// popoverPopup показывает GtkPopover (gotk3 не оборачивает gtk_popover_popup;
// gtk_widget_show тоже открывает, но без анимации).
func popoverPopup(p *gtk.Popover) {
	if p == nil {
		return
	}
	C.gtk_popover_popup(
		(*C.GtkPopover)(unsafe.Pointer(p.ToWidget().Native())),
	)
}
