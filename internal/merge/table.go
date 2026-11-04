// SPDX-License-Identifier: AGPL-3.0-or-later

package merge

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ErrNoEmailColumn is returned when no e-mail column was found.
var ErrNoEmailColumn = errors.New("merge: no e-mail column found")

// ParseTable reads recipients from a CSV/TSV or XLSX file (detected by
// content). The first row holds the column names; the e-mail column is
// found by name or, failing that, by content.
func ParseTable(data []byte) ([]Recipient, []string, error) {
	if bytes.HasPrefix(data, []byte("PK\x03\x04")) {
		rows, err := readXLSX(data)
		if err != nil {
			return nil, nil, err
		}
		return fromRows(rows)
	}
	return ParseCSV(data)
}

// ParseCSV reads a CSV with ",", ";" or tab delimiters (UTF-8, optional BOM;
// Latin-1 is converted).
func ParseCSV(data []byte) ([]Recipient, []string, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if !utf8.Valid(data) {
		var b strings.Builder
		for _, c := range data {
			b.WriteRune(rune(c))
		}
		data = []byte(b.String())
	}
	first, _, _ := bytes.Cut(data, []byte("\n"))
	delim := ','
	best := bytes.Count(first, []byte(","))
	for _, d := range []rune{';', '\t'} {
		if n := bytes.Count(first, []byte(string(d))); n > best {
			delim, best = d, n
		}
	}
	r := csv.NewReader(bytes.NewReader(data))
	r.Comma = delim
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	r.TrimLeadingSpace = true
	rows, err := r.ReadAll()
	if err != nil {
		return nil, nil, fmt.Errorf("merge: CSV: %w", err)
	}
	return fromRows(rows)
}

func fromRows(rows [][]string) ([]Recipient, []string, error) {
	if len(rows) < 2 {
		return nil, nil, errors.New("merge: the table needs a header row and at least one recipient")
	}
	header := make([]string, len(rows[0]))
	emailCol, nameCol := -1, -1
	for i, h := range rows[0] {
		header[i] = strings.ToLower(strings.TrimSpace(h))
		switch canonical(header[i]) {
		case "email":
			if emailCol < 0 {
				emailCol = i
			}
		case "name":
			nameCol = i
		}
	}
	if emailCol < 0 {
		// Detect by content.
		for i := range header {
			if i < len(rows[1]) && strings.Contains(rows[1][i], "@") {
				emailCol = i
				break
			}
		}
	}
	if emailCol < 0 {
		return nil, header, ErrNoEmailColumn
	}
	var out []Recipient
	for _, row := range rows[1:] {
		if emailCol >= len(row) || strings.TrimSpace(row[emailCol]) == "" {
			continue
		}
		rc := Recipient{Email: strings.TrimSpace(row[emailCol]), Fields: map[string]string{}}
		for i, v := range row {
			if i < len(header) && header[i] != "" {
				rc.Fields[header[i]] = strings.TrimSpace(v)
			}
		}
		if nameCol >= 0 && nameCol < len(row) {
			rc.Name = strings.TrimSpace(row[nameCol])
		}
		out = append(out, rc)
	}
	return out, header, nil
}

// ==== XLSX (first worksheet, shared and inline strings) ===========================

func readXLSX(data []byte) ([][]string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("merge: XLSX: %w", err)
	}
	open := func(name string) ([]byte, error) {
		for _, f := range zr.File {
			if f.Name == name {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				return io.ReadAll(io.LimitReader(rc, 64<<20))
			}
		}
		return nil, nil
	}
	var shared []string
	if b, err := open("xl/sharedStrings.xml"); err != nil {
		return nil, err
	} else if b != nil {
		var sst struct {
			SI []struct {
				T string `xml:"t"`
				R []struct {
					T string `xml:"t"`
				} `xml:"r"`
			} `xml:"si"`
		}
		if err := xml.Unmarshal(b, &sst); err != nil {
			return nil, fmt.Errorf("merge: XLSX shared strings: %w", err)
		}
		for _, si := range sst.SI {
			s := si.T
			for _, r := range si.R {
				s += r.T
			}
			shared = append(shared, s)
		}
	}
	sheet, err := open("xl/worksheets/sheet1.xml")
	if err != nil || sheet == nil {
		return nil, errors.New("merge: XLSX without first worksheet")
	}
	var ws struct {
		Rows []struct {
			C []struct {
				R  string `xml:"r,attr"`
				T  string `xml:"t,attr"`
				V  string `xml:"v"`
				IS struct {
					T string `xml:"t"`
				} `xml:"is"`
			} `xml:"c"`
		} `xml:"sheetData>row"`
	}
	if err := xml.Unmarshal(sheet, &ws); err != nil {
		return nil, fmt.Errorf("merge: XLSX worksheet: %w", err)
	}
	var rows [][]string
	for _, r := range ws.Rows {
		var row []string
		for _, c := range r.C {
			col := colIndex(c.R)
			for len(row) < col {
				row = append(row, "")
			}
			v := c.V
			switch c.T {
			case "s":
				if i, err := strconv.Atoi(c.V); err == nil && i >= 0 && i < len(shared) {
					v = shared[i]
				}
			case "inlineStr":
				v = c.IS.T
			}
			row = append(row, v)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// colIndex converts "C7" to 2.
func colIndex(ref string) int {
	n := 0
	for _, r := range ref {
		if r < 'A' || r > 'Z' {
			break
		}
		n = n*26 + int(r-'A'+1)
	}
	if n == 0 {
		return 0
	}
	return n - 1
}
