package main

// dbg_tree.go — диагностический дампер UI (v1.13.2, встраиваемый инструмент).
// Активируется переменной окружения RA_DEBUG_UI=1:
//   • dbgInstallLog — CRITHIT-сообщения Gtk-CRITICAL в stderr;
//   • dbgDumpC      — дерево виджетов (класс, аллокация, видимость)
//     напрямую через C GTK — независимо от типизации gotk3.
// В обычной работе ничего не выводит и не влияет на поведение.

/*
#cgo pkg-config: gtk+-3.0
#include <gtk/gtk.h>
#include <string.h>

static void dbg_walk(GtkWidget *w, int depth);

static void dbg_print(GtkWidget *w, int depth) {
    const char *cls = G_OBJECT_TYPE_NAME(w);
    GtkAllocation a;
    gtk_widget_get_allocation(w, &a);
    gboolean vis = gtk_widget_get_visible(w);
    gint mnw = -1, natw = -1, mnh = -1, nath = -1;
    gtk_widget_get_preferred_width(w, &mnw, &natw);
    gtk_widget_get_preferred_height(w, &mnh, &nath);
    fprintf(stderr, "DBG%*s%s alloc=%dx%d+%d+%d vis=%d minw=%d ntw=%d minh=%d\n",
            depth * 2, "", cls, a.width, a.height, a.x, a.y, vis, mnw, natw, mnh);
    fflush(stderr);
}

static void dbg_walk(GtkWidget *w, int depth) {
    if (depth > 24) return;
    dbg_print(w, depth);
    if (!GTK_IS_CONTAINER(w)) return;
    GList *kids = gtk_container_get_children(GTK_CONTAINER(w));
    for (GList *l = kids; l != NULL; l = l->next)
        dbg_walk(GTK_WIDGET(l->data), depth + 1);
    if (kids) g_list_free(kids);
}

static void dbg_dump_c(GtkWidget *w) { dbg_walk(w, 1); }

// temporary: backtrace on Gtk-CRITICAL (find double-parented pack)
static void dbg_log_handler(const gchar *domain, GLogLevelFlags level, const gchar *msg, gpointer ud) {
    fprintf(stderr, "CRITHIT %s: %s\n", domain ? domain : "?", msg);
    fflush(stderr);
}
static void dbg_install_log(void) {
    g_log_set_handler("Gtk", G_LOG_LEVEL_CRITICAL | G_LOG_FLAG_FATAL, dbg_log_handler, NULL);
    g_log_set_handler("GLib-GObject", G_LOG_LEVEL_CRITICAL | G_LOG_FLAG_FATAL, dbg_log_handler, NULL);
}
*/
import "C"

import (
        "unsafe"

        "github.com/gotk3/gotk3/gtk"
)

func dbgInstallLog() { C.dbg_install_log() }

func dbgDumpC(w gtk.IWidget) {
        C.dbg_dump_c((*C.GtkWidget)(unsafe.Pointer(w.ToWidget().Native())))
}
