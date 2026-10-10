package etl

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// RowIter yields rows one at a time and returns io.EOF after the last.
type RowIter func() (Row, error)

// NewRowIter reads rows from r in the given format without holding them all:
// csv (the first line is the header), jsonl (one object a line) or json (an
// array of objects).
func NewRowIter(format string, r io.Reader) (RowIter, error) {
	switch format {
	case "csv":
		cr := csv.NewReader(bufio.NewReaderSize(r, 1<<20))
		cr.FieldsPerRecord = -1
		cr.TrimLeadingSpace = true
		cr.ReuseRecord = false
		head, err := cr.Read()
		if err != nil {
			return nil, fmt.Errorf("%w: csv header: %v", ErrInvalid, err)
		}
		for i := range head {
			head[i] = strings.TrimSpace(head[i])
		}
		line := 1
		return func() (Row, error) {
			rec, err := cr.Read()
			line++
			if err == io.EOF {
				return nil, io.EOF
			}
			if err != nil {
				return nil, fmt.Errorf("%w: csv row %d: %v", ErrInvalid, line, err)
			}
			row := make(Row, len(head))
			for i, h := range head {
				if i < len(rec) {
					row[h] = rec[i]
				}
			}
			return row, nil
		}, nil
	case "jsonl":
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		n := 0
		return func() (Row, error) {
			for sc.Scan() {
				n++
				line := bytes.TrimSpace(sc.Bytes())
				if len(line) == 0 {
					continue
				}
				var row Row
				if err := json.Unmarshal(line, &row); err != nil {
					return nil, fmt.Errorf("%w: jsonl line %d: %v", ErrInvalid, n, err)
				}
				return row, nil
			}
			if err := sc.Err(); err != nil {
				return nil, err
			}
			return nil, io.EOF
		}, nil
	case "json":
		dec := json.NewDecoder(bufio.NewReaderSize(r, 1<<20))
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("%w: json: %v", ErrInvalid, err)
		}
		if d, ok := tok.(json.Delim); !ok || d != '[' {
			return nil, fmt.Errorf("%w: json: expected an array of objects", ErrInvalid)
		}
		n := 0
		return func() (Row, error) {
			if !dec.More() {
				if _, err := dec.Token(); err != nil {
					return nil, fmt.Errorf("%w: json: %v", ErrInvalid, err)
				}
				return nil, io.EOF
			}
			n++
			var row Row
			if err := dec.Decode(&row); err != nil {
				return nil, fmt.Errorf("%w: json element %d: %v", ErrInvalid, n, err)
			}
			return row, nil
		}, nil
	}
	return nil, fmt.Errorf("%w: unknown format %q", ErrInvalid, format)
}

// SliceIter iterates rows already in memory.
func SliceIter(rows []Row) RowIter {
	i := 0
	return func() (Row, error) {
		if i >= len(rows) {
			return nil, io.EOF
		}
		i++
		return rows[i-1], nil
	}
}
