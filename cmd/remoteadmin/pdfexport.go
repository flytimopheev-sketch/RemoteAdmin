package main

import (
        "fmt"
        "path/filepath"
        "strings"
        "time"

        "github.com/go-pdf/fpdf"
        "github.com/gotk3/gotk3/gtk"
)

// ---------- PDF export ----------
//
// Экспорт журнала отчётов в PDF. Для кириллицы TrueType-шрифт встраивается
// в документ (AddUTF8Font); ищем DejaVu Sans в стандартных местах, при
// отсутствии — любой .ttf из /usr/share/fonts. Если шрифта нет вообще,
// пользователь получает понятное предупреждение (экспорт HTML/CSV работает
// без шрифтов).

var pdfFontCandidates = []string{
        "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
        "/usr/share/fonts/dejavu/DejaVuSans.ttf",
        "/usr/share/fonts/dejavu-sans-fonts/DejaVuSans.ttf",
        "/usr/share/fonts/truetype/noto/NotoSans-Regular.ttf",
        "/usr/share/fonts/google-noto-sans-fonts/NotoSans-Regular.ttf",
        "/usr/share/fonts/truetype/liberation/LiberationSans-Regular.ttf",
}

func findPDFFont() string {
        for _, p := range pdfFontCandidates {
                if fileExists(p) {
                        return p
                }
        }
        // последний шанс: первый попавшийся ttf
        matches, _ := filepath.Glob("/usr/share/fonts/**/*.ttf")
        for _, m := range matches {
                return m
        }
        return ""
}

func exportReportPDF() {
        if len(store.State.Reports) == 0 {
                warnDialog(app.window, "Журнал пуст — нечего экспортировать")
                return
        }
        font := findPDFFont()
        if font == "" {
                warnDialog(app.window, "Не найден шрифт TTF (DejaVu/Noto) — кириллица в PDF невозможна.\nИспользуйте экспорт HTML или CSV.")
                return
        }
        fn := fileChooser(app.window, "Сохранить PDF-отчёт", gtk.FILE_CHOOSER_ACTION_SAVE, "remoteadmin_report.pdf")
        if fn == "" {
                return
        }
        if err := writeReportPDF(fn, font); err != nil {
                errDialog(app.window, "Экспорт PDF: "+err.Error())
                return
        }
        logf("ok", "📄 PDF-отчёт: %s", fn)
}

func writeReportPDF(path, fontPath string) error {
        pdf := fpdf.New("P", "mm", "A4", "")
        pdf.SetMargins(12, 12, 12)
        pdf.SetAutoPageBreak(true, 14)
        pdf.AddUTF8Font("sans", "", fontPath)
        pdf.SetTitle("RemoteAdmin — отчёт", true)
        pdf.SetAuthor(AppName+" "+AppVersion, true)

        // статистика
        okN, failN, warnN := 0, 0, 0
        hosts := map[string]bool{}
        for _, r := range store.State.Reports {
                switch r.Status {
                case "OK":
                        okN++
                case "FAIL":
                        failN++
                case "WARN":
                        warnN++
                }
                hosts[r.Host] = true
        }

        pdf.AddPage()

        // шапка
        pdf.SetFont("sans", "", 20)
        pdf.SetTextColor(20, 24, 31)
        pdf.Cell(0, 12, "RemoteAdmin — отчёт о выполнении")
        pdf.Ln(9)
        pdf.SetFont("sans", "", 9)
        pdf.SetTextColor(95, 99, 104)
        pdf.Cell(0, 6, fmt.Sprintf("Сформирован %s  •  RemoteAdmin %s  •  записей: %d, хостов: %d",
                time.Now().Format("02.01.2006 15:04:05"), AppVersion, len(store.State.Reports), len(hosts)))
        pdf.Ln(9)

        // карточки-счётчики
        drawStatCard := func(x float64, w float64, n int, label string, r, g, b int) {
                pdf.SetFillColor(246, 247, 249)
                pdf.RoundedRect(x, pdf.GetY(), w, 14, 2, "1234", "F")
                pdf.SetXY(x+3, pdf.GetY()+1)
                pdf.SetFont("sans", "", 13)
                pdf.SetTextColor(r, g, b)
                pdf.CellFormat(w-6, 6, fmt.Sprintf("%d", n), "", 2, "L", false, 0, "")
                pdf.SetX(x + 3)
                pdf.SetFont("sans", "", 8)
                pdf.SetTextColor(95, 99, 104)
                pdf.Cell(w-6, 5, label)
        }
        y0 := pdf.GetY()
        pageW, pageH := pdf.GetPageSize()
        w := (pageW - 24 - 12) / 4
        drawStatCard(12, w, len(store.State.Reports), "Всего", 32, 41, 57)
        pdf.SetXY(12+w+4, y0)
        drawStatCard(16+w, w, okN, "Успешно", 24, 128, 56)
        pdf.SetXY(16+2*w, y0)
        drawStatCard(20+2*w, w, warnN, "Предупреждения", 232, 113, 10)
        pdf.SetXY(20+3*w, y0)
        drawStatCard(24+3*w, w, failN, "Ошибки", 217, 48, 37)
        pdf.Ln(18)

        // таблица
        headFill := func() {
                pdf.SetFillColor(31, 41, 51)
                pdf.SetTextColor(255, 255, 255)
                pdf.SetFont("sans", "", 9)
        }
        headFill()
        pdf.SetXY(12, pdf.GetY())
        colW := []float64{27, 52, 30, 20}
        sum := 0.0
        for _, c := range colW {
                sum += c
        }
        colW[4] = pageW - 24 - sum // вывод — остальная ширина
        headers := []string{"Время", "Задача", "Хост", "Статус", "Вывод"}
        for i, h := range headers {
                pdf.CellFormat(colW[i], 7, h, "", 0, "L", true, 0, "")
        }
        pdf.Ln(-1)

        pdf.SetFont("sans", "", 8)
        for _, r := range store.State.Reports {
                // цвет статуса
                switch r.Status {
                case "OK":
                        pdf.SetTextColor(24, 128, 56)
                case "FAIL":
                        pdf.SetTextColor(217, 48, 37)
                default:
                        pdf.SetTextColor(232, 113, 10)
                }
                out := strings.ReplaceAll(r.Output, "\r", "")
                if len(out) > 300 {
                        out = out[:300] + "…"
                }
                h := 6.0
                if pdf.GetStringWidth(out) > colW[4]-3 {
                        h = 12.0
                }
                // чередование фона
                pdf.SetFillColor(246, 247, 249)
                bg := pdf.PageCount()%2 == 0
                pdf.SetX(12)
                pdf.CellFormat(colW[0], h, r.Time, "", 0, "L", bg, 0, "")
                pdf.CellFormat(colW[1], h, truncateCell(r.Task, colW[1]-2, pdf), "", 0, "L", bg, 0, "")
                pdf.CellFormat(colW[2], h, truncateCell(r.Host, colW[2]-2, pdf), "", 0, "L", bg, 0, "")
                pdf.CellFormat(colW[3], h, r.Status, "", 0, "L", bg, 0, "")
                pdf.MultiCell(colW[4], h, out, "", "L", bg)
                pdf.SetTextColor(32, 33, 36)
                if pdf.GetY() > pageH-40 {
                        pdf.AddPage()
                        headFill()
                        pdf.SetX(12)
                        for i, h2 := range headers {
                                pdf.CellFormat(colW[i], 7, h2, "", 0, "L", true, 0, "")
                        }
                        pdf.Ln(-1)
                        pdf.SetFont("sans", "", 8)
                        pdf.SetTextColor(32, 33, 36)
                }
        }

        return pdf.OutputFileAndClose(path)
}

func truncateCell(s string, maxW float64, pdf *fpdf.Fpdf) string {
        if pdf.GetStringWidth(s) <= maxW {
                return s
        }
        for len(s) > 4 && pdf.GetStringWidth(s+"…") > maxW {
                r := []rune(s)
                s = string(r[:len(r)-2])
        }
        return s + "…"
}
