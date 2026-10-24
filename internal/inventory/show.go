package inventory

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// showColumns is the fixed inventory show table layout.
var showColumns = []string{
	"name",
	"address",
	"port",
	"discovered",
	"vendor",
	"model",
	"os_version",
	"catalog_key",
	"last_error",
}

// FormatTable renders target rows as a simple aligned text table.
func FormatTable(rows []map[string]any) string {
	widths := make([]int, len(showColumns))
	for i, column := range showColumns {
		widths[i] = utf8.RuneCountInString(column)
	}
	matrix := make([][]string, len(rows))
	for r, row := range rows {
		cells := make([]string, len(showColumns))
		for i, column := range showColumns {
			cells[i] = showCell(row, column)
			if n := utf8.RuneCountInString(cells[i]); n > widths[i] {
				widths[i] = n
			}
		}
		matrix[r] = cells
	}
	lines := make([]string, 0, len(rows)+2)
	lines = append(lines, padCells(showColumns, widths))
	dashes := make([]string, len(widths))
	for i, width := range widths {
		dashes[i] = strings.Repeat("-", width)
	}
	lines = append(lines, strings.Join(dashes, "  "))
	for _, cells := range matrix {
		lines = append(lines, padCells(cells, widths))
	}
	return strings.Join(lines, "\n")
}

func showCell(row map[string]any, column string) string {
	if column == "discovered" {
		if discovered, ok := row["discovered"].(int64); ok && discovered != 0 {
			return "yes"
		}
		return "no"
	}
	value := row[column]
	if value == nil {
		return "-"
	}
	if text, ok := value.(string); ok {
		if text == "" {
			return "-"
		}
		return text
	}
	return fmt.Sprint(value)
}

func padCells(cells []string, widths []int) string {
	padded := make([]string, len(cells))
	for i, cell := range cells {
		padded[i] = cell + strings.Repeat(" ", widths[i]-utf8.RuneCountInString(cell))
	}
	return strings.Join(padded, "  ")
}
