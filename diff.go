package main

import (
	"fmt"
	"strings"

	"github.com/gotk3/gotk3/gtk"
)

// ---------- output diff between hosts ----------

// diffLines builds a simple unified diff between two outputs
// using LCS. Returns lines prefixed with " ", "-", "+".
func diffLines(a, b []string) []string {
	n, m := len(a), len(b)
	// LCS table (guard size)
	if n*m > 4_000_000 {
		// too big: fall back to naive side-by-side markers
		return naiveDiff(a, b)
	}
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	var out []string
	i, j := 0, 0
	for i < n && j < m {
		if a[i] == b[j] {
			out = append(out, "  "+a[i])
			i++
			j++
		} else if lcs[i+1][j] >= lcs[i][j+1] {
			out = append(out, "- "+a[i])
			i++
		} else {
			out = append(out, "+ "+b[j])
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, "- "+a[i])
	}
	for ; j < m; j++ {
		out = append(out, "+ "+b[j])
	}
	return out
}

func naiveDiff(a, b []string) []string {
	var out []string
	for _, l := range a {
		out = append(out, "- "+l)
	}
	for _, l := range b {
		out = append(out, "+ "+l)
	}
	return out
}

// showDiffDialog displays diff of last outputs between two hosts.
func showDiffDialog(hostA, hostB string) {
	outA, okA := store.State.LastOutput[hostA]
	outB, okB := store.State.LastOutput[hostB]
	if !okA || !okB {
		warnDialog(app.window, "Нет сохранённого вывода для сравнения.\nСначала выполните команду или playbook.")
		return
	}
	lines := diffLines(
		strings.Split(strings.TrimRight(outA, "\n"), "\n"),
		strings.Split(strings.TrimRight(outB, "\n"), "\n"),
	)
	d, err := gtk.DialogNewWithButtons(
		fmt.Sprintf("Различия вывода: %s ↔ %s", hostA, hostB),
		app.window, gtk.DIALOG_MODAL,
		[]interface{}{"Закрыть", gtk.RESPONSE_CLOSE},
	)
	if err != nil {
		return
	}
	defer d.Destroy()
	d.SetDefaultSize(760, 520)
	box, _ := d.GetContentArea()
	tv, _ := gtk.TextViewNew()
	tv.SetEditable(false)
	tv.SetMonospace(true)
	tv.SetWrapMode(gtk.WRAP_NONE)
	buf, _ := tv.GetBuffer()
	buf.CreateTag("add", map[string]interface{}{"foreground": "#4CAF50"})
	buf.CreateTag("del", map[string]interface{}{"foreground": "#f44336"})
	iter := buf.GetStartIter()
	if len(lines) == 0 {
		buf.Insert(iter, "Вывод идентичен.\n")
	}
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, "+ "):
			buf.InsertWithTagByName(iter, l+"\n", "add")
		case strings.HasPrefix(l, "- "):
			buf.InsertWithTagByName(iter, l+"\n", "del")
		default:
			buf.Insert(iter, l+"\n")
		}
	}
	sc := scrolled(tv, 0)
	sc.SetVExpand(true)
	box.PackStart(sc, true, true, 4)
	box.ShowAll()
	d.Run()
}
