package output

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

const tableColumnGap = "  "

func WriteTable(w io.Writer, headers []string, rows [][]string) error {
	if len(headers) == 0 {
		return nil
	}
	widths := make([]int, len(headers))
	for i, header := range headers {
		headers[i] = cleanCell(header)
		widths[i] = cellWidth(headers[i])
	}
	for _, row := range rows {
		for i := 0; i < len(headers) && i < len(row); i++ {
			width := cellWidth(cleanCell(row[i]))
			if width > widths[i] {
				widths[i] = width
			}
		}
	}
	if err := writeTableRow(w, headers, widths); err != nil {
		return err
	}
	for _, row := range rows {
		cells := make([]string, len(headers))
		for i := range headers {
			if i < len(row) {
				cells[i] = cleanCell(row[i])
			}
		}
		if err := writeStyledTableRow(w, headers, cells, widths); err != nil {
			return err
		}
	}
	return nil
}

func writeTableRow(w io.Writer, cells []string, widths []int) error {
	for i, cell := range cells {
		if i > 0 {
			if _, err := io.WriteString(w, tableColumnGap); err != nil {
				return err
			}
		}
		if i == len(cells)-1 {
			if _, err := io.WriteString(w, cell); err != nil {
				return err
			}
			continue
		}
		if _, err := io.WriteString(w, cell); err != nil {
			return err
		}
		if _, err := io.WriteString(w, strings.Repeat(" ", widths[i]-cellWidth(cell))); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(w)
	return err
}

func writeStyledTableRow(w io.Writer, headers, cells []string, widths []int) error {
	styled := make([]string, len(cells))
	copy(styled, cells)
	for i := range styled {
		if i < len(headers) {
			styled[i] = ColorizeTableCell(w, headers[i], styled[i])
		}
	}
	for i, cell := range styled {
		if i > 0 {
			if _, err := io.WriteString(w, tableColumnGap); err != nil {
				return err
			}
		}
		if i == len(styled)-1 {
			if _, err := io.WriteString(w, cell); err != nil {
				return err
			}
			continue
		}
		if _, err := io.WriteString(w, cell); err != nil {
			return err
		}
		if _, err := io.WriteString(w, strings.Repeat(" ", widths[i]-cellWidth(cells[i]))); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(w)
	return err
}

func cleanCell(value string) string {
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "\t", " ")
	return value
}

func cellWidth(value string) int {
	return utf8.RuneCountInString(value)
}
