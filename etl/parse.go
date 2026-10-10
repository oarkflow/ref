package etl

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// ParseRows reads rows in the given format: csv (first line is the header),
// jsonl (one object per line) or json (an array of objects).
func ParseRows(format string, data []byte) ([]Row, error) {
	switch format {
	case "csv":
		r := csv.NewReader(bytes.NewReader(data))
		r.FieldsPerRecord = -1
		r.TrimLeadingSpace = true
		head, err := r.Read()
		if err != nil {
			return nil, fmt.Errorf("%w: csv header: %v", ErrInvalid, err)
		}
		var rows []Row
		for {
			rec, err := r.Read()
			if err == io.EOF {
				return rows, nil
			}
			if err != nil {
				return nil, fmt.Errorf("%w: csv row %d: %v", ErrInvalid, len(rows)+2, err)
			}
			row := Row{}
			for i, h := range head {
				if i < len(rec) {
					row[strings.TrimSpace(h)] = rec[i]
				}
			}
			rows = append(rows, row)
		}
	case "jsonl":
		var rows []Row
		sc := bufio.NewScanner(bytes.NewReader(data))
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		for n := 1; sc.Scan(); n++ {
			line := bytes.TrimSpace(sc.Bytes())
			if len(line) == 0 {
				continue
			}
			var row Row
			if err := json.Unmarshal(line, &row); err != nil {
				return nil, fmt.Errorf("%w: jsonl line %d: %v", ErrInvalid, n, err)
			}
			rows = append(rows, row)
		}
		return rows, sc.Err()
	case "json":
		var rows []Row
		if err := json.Unmarshal(data, &rows); err != nil {
			return nil, fmt.Errorf("%w: json: %v", ErrInvalid, err)
		}
		return rows, nil
	}
	return nil, fmt.Errorf("%w: unknown format %q", ErrInvalid, format)
}

// RowsHash is the SHA-256 of the rows' canonical JSON (map keys are sorted by
// encoding/json), used for checkpoints and idempotency checks.
func RowsHash(rows []Row) string {
	b, _ := json.Marshal(rows)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
