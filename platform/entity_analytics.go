package platform

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Entity analytics. An entity declared `analytics true` answers
//
//	GET {path}/-/analytics?metrics=count,sum:budget,avg:cost&group_by=status,unit&bucket=month&bucket_field=created_at
//
// with the same filters, search and scoping as list. The database does the
// grouping, so rows are never loaded: every metric is computed from SUM,
// COUNT, MIN and MAX per group, which keeps decimal (integer minor unit)
// columns exact and lets a weekly bucket be rolled up from daily groups in
// Go, since weeks have no portable SQL. Dates and datetimes are stored as
// ISO text, so day and month buckets are a portable SUBSTR. The groups are
// capped: a query that would return more is refused rather than truncated.

const (
	entityAnalyticsMaxGroups  = 1000
	entityAnalyticsMaxMetrics = 10
)

// analyticsMetric is one requested metric: fn over field ("" for count(*)).
type analyticsMetric struct {
	fn, field string
	slot      int // the field's position among the queried fields
}

func (m analyticsMetric) key() string {
	if m.field == "" {
		return m.fn
	}
	return m.fn + "_" + m.field
}

// analyticsAcc accumulates one field of one group.
type analyticsAcc struct {
	count        int64
	isum, lo, hi int64 // integer and decimal fields: exact
	fsum, flo    float64
	fhi          float64
}

type analyticsGroup struct {
	bucket any
	keys   []any
	n      int64
	fields []analyticsAcc
}

func (rt *entityRuntime) analytics(ctx *ActionContext) (any, error) {
	p := rt.plan
	params := queryParams(ctx)
	get := func(k string) string {
		if len(params[k]) > 0 {
			return strings.TrimSpace(params[k][0])
		}
		return ""
	}
	numeric := func(col string) *EntityColumn {
		if c := p.columns[col]; c != nil && !c.Hidden && (c.Kind == "integer" || c.Kind == "number" || c.Kind == "decimal") {
			return c
		}
		return nil
	}

	// Metrics: count, or fn:field with fn count, sum, avg, min or max.
	var (
		metrics []analyticsMetric
		fields  []string
		names   []string
	)
	for _, raw := range strings.Split(orDefault(get("metrics"), "count"), ",") {
		raw = strings.TrimSpace(raw)
		fn, field, _ := strings.Cut(raw, ":")
		if !slices.Contains([]string{"count", "sum", "avg", "min", "max"}, fn) || (fn != "count" && field == "") {
			return nil, invalidInput("metric %q is not count or sum|avg|min|max|count:<field>", raw)
		}
		if field != "" && numeric(field) == nil {
			return nil, invalidInput("metric %q needs a numeric field", raw)
		}
		if slices.Contains(names, raw) {
			continue
		}
		names = append(names, raw)
		m := analyticsMetric{fn: fn, field: field, slot: -1}
		if field != "" {
			if m.slot = slices.Index(fields, field); m.slot < 0 {
				fields = append(fields, field)
				m.slot = len(fields) - 1
			}
		}
		metrics = append(metrics, m)
	}
	if len(metrics) > entityAnalyticsMaxMetrics {
		return nil, invalidInput("at most %d metrics", entityAnalyticsMaxMetrics)
	}

	// Up to two group-by columns.
	var groupBy []string
	if raw := get("group_by"); raw != "" {
		for _, col := range strings.Split(raw, ",") {
			col = strings.TrimSpace(col)
			c := p.columns[col]
			if ((c == nil || c.Hidden || c.Kind == "json") && col != "created_by") || slices.Contains(groupBy, col) {
				return nil, invalidInput("cannot group by %q", col)
			}
			groupBy = append(groupBy, col)
		}
		if len(groupBy) > 2 {
			return nil, invalidInput("group by at most 2 columns")
		}
	}

	// A time bucket on a date or datetime column.
	bucket, bucketField := get("bucket"), ""
	width := 0
	switch bucket {
	case "":
		if get("bucket_field") != "" {
			return nil, invalidInput("bucket_field needs a bucket (day, week or month)")
		}
	case "day", "week":
		width = 10
	case "month":
		width = 7
	default:
		return nil, invalidInput("bucket must be day, week or month")
	}
	if bucket != "" {
		bucketField = orDefault(get("bucket_field"), "created_at")
		c := p.columns[bucketField]
		if !(c != nil && !c.Hidden && (c.Kind == "date" || c.Kind == "datetime")) && bucketField != "created_at" && bucketField != "updated_at" {
			return nil, invalidInput("cannot bucket by %q: it is not a date or datetime column", bucketField)
		}
	}

	where, args, err := rt.where(ctx, "metrics", "group_by", "bucket", "bucket_field")
	if err != nil {
		return nil, err
	}
	var keys, cols []string
	if bucket != "" {
		keys = append(keys, fmt.Sprintf("SUBSTR(%s, 1, %d)", bucketField, width))
	}
	keys = append(keys, groupBy...)
	for i, k := range keys {
		cols = append(cols, fmt.Sprintf("%s AS k%d", k, i))
	}
	cols = append(cols, "COUNT(*) AS n")
	for i, f := range fields {
		cols = append(cols, fmt.Sprintf("COUNT(%s) AS c%d, SUM(%[1]s) AS s%[2]d, MIN(%[1]s) AS lo%[2]d, MAX(%[1]s) AS hi%[2]d", f, i))
	}
	// A week rolls up to seven days of groups.
	maxRows := entityAnalyticsMaxGroups
	if bucket == "week" {
		maxRows *= 7
	}
	stmt := "SELECT " + strings.Join(cols, ", ") + " FROM " + p.table + where
	if len(keys) > 0 {
		stmt += " GROUP BY " + strings.Join(keys, ", ") + " ORDER BY " + strings.Join(keys, ", ")
		args = append(args, maxRows+1)
		stmt += fmt.Sprintf(" LIMIT $%d", len(args))
	}
	rows, err := queryRows(ctx.Context, rt.db.Reader(), rebind(rt.db.Dialect, stmt), args)
	if err != nil {
		return nil, databaseFailure(err)
	}
	tooMany := invalidInput("the analytics would return more than %d groups; filter, group by less or use a coarser bucket", entityAnalyticsMaxGroups)
	if len(rows) > maxRows {
		return nil, tooMany
	}

	// Accumulate: one group per row, except weeks, which merge their days.
	var groups []*analyticsGroup
	index := map[string]*analyticsGroup{}
	for _, row := range rows {
		g := &analyticsGroup{fields: make([]analyticsAcc, len(fields))}
		k := 0
		if bucket != "" {
			g.bucket = row["k0"]
			if bucket == "week" {
				g.bucket = isoWeekStart(g.bucket)
			}
			k = 1
		}
		for i, col := range groupBy {
			g.keys = append(g.keys, rt.decode(map[string]any{col: row[fmt.Sprintf("k%d", i+k)]})[col])
		}
		if bucket == "week" {
			id := fmt.Sprintf("%v\x00%v", g.bucket, g.keys)
			if prior := index[id]; prior != nil {
				g = prior
			} else {
				index[id] = g
				groups = append(groups, g)
			}
		} else {
			groups = append(groups, g)
		}
		n, _ := sqlInt(row["n"])
		g.n += n
		for i, f := range fields {
			if err := g.fields[i].add(p.columns[f].Kind, row, i); err != nil {
				return nil, databaseFailure(err)
			}
		}
	}
	if len(groups) > entityAnalyticsMaxGroups {
		return nil, tooMany
	}
	if bucket == "week" {
		// Days of one week arrive apart: order as SQL would have.
		slices.SortStableFunc(groups, func(a, b *analyticsGroup) int {
			if c := compareGroupValues(a.bucket, b.bucket); c != 0 {
				return c
			}
			for i := range a.keys {
				if c := compareGroupValues(a.keys[i], b.keys[i]); c != 0 {
					return c
				}
			}
			return 0
		})
	}

	out := make([]any, 0, len(groups))
	for _, g := range groups {
		values := map[string]any{}
		for _, m := range metrics {
			values[m.key()] = analyticsValue(m, g, p.columns[m.field])
		}
		item := map[string]any{"values": values}
		if bucket != "" {
			item["bucket"] = g.bucket
		}
		if len(groupBy) > 0 {
			group := map[string]any{}
			for i, col := range groupBy {
				group[col] = g.keys[i]
			}
			item["group"] = group
		}
		out = append(out, item)
	}
	if groupBy == nil {
		groupBy = []string{}
	}
	return map[string]any{"metrics": names, "group_by": groupBy, "bucket": bucket, "bucket_field": bucketField,
		"groups": len(out), "rows": out}, nil
}

// add folds one SQL row's figures for field slot i into the accumulator.
func (a *analyticsAcc) add(kind string, row map[string]any, i int) error {
	c, _ := sqlInt(row[fmt.Sprintf("c%d", i)])
	if c == 0 {
		return nil
	}
	s, lo, hi := row[fmt.Sprintf("s%d", i)], row[fmt.Sprintf("lo%d", i)], row[fmt.Sprintf("hi%d", i)]
	first := a.count == 0
	a.count += c
	if kind == "number" {
		fs, ok1 := ToFloat(s)
		flo, ok2 := ToFloat(lo)
		fhi, ok3 := ToFloat(hi)
		if !ok1 || !ok2 || !ok3 {
			return fmt.Errorf("unexpected aggregate values %v, %v, %v", s, lo, hi)
		}
		a.fsum += fs
		if first || flo < a.flo {
			a.flo = flo
		}
		if first || fhi > a.fhi {
			a.fhi = fhi
		}
		return nil
	}
	is, ok1 := sqlInt(s)
	ilo, ok2 := sqlInt(lo)
	ihi, ok3 := sqlInt(hi)
	if !ok1 || !ok2 || !ok3 {
		return fmt.Errorf("unexpected aggregate values %v, %v, %v", s, lo, hi)
	}
	a.isum += is
	if first || ilo < a.lo {
		a.lo = ilo
	}
	if first || ihi > a.hi {
		a.hi = ihi
	}
	return nil
}

// analyticsValue renders a metric of a group in its column's kind: decimals
// as exact strings (an average rounded half away from zero to the scale),
// integers as integers, numbers as floats. A metric over no values is null.
func analyticsValue(m analyticsMetric, g *analyticsGroup, c *EntityColumn) any {
	if m.field == "" {
		return g.n
	}
	a := g.fields[m.slot]
	if m.fn == "count" {
		return a.count
	}
	if a.count == 0 {
		return nil
	}
	if c.Kind == "number" {
		switch m.fn {
		case "sum":
			return a.fsum
		case "avg":
			return a.fsum / float64(a.count)
		case "min":
			return a.flo
		}
		return a.fhi
	}
	var v int64
	switch m.fn {
	case "sum":
		v = a.isum
	case "min":
		v = a.lo
	case "max":
		v = a.hi
	case "avg":
		if c.Kind == "integer" {
			return float64(a.isum) / float64(a.count)
		}
		v = roundDiv(a.isum, a.count)
	}
	if c.Kind == "decimal" {
		return formatDecimal(v, c.Scale)
	}
	return v
}

// roundDiv is n/d rounded half away from zero (d > 0).
func roundDiv(n, d int64) int64 {
	q, r := n/d, n%d
	if r < 0 {
		r = -r
	}
	if 2*r >= d {
		if n < 0 {
			q--
		} else {
			q++
		}
	}
	return q
}

// sqlInt reads an exact integer from a driver value: SUM of a BIGINT is an
// int64 on SQLite but a numeric (text) on PostgreSQL and a DECIMAL on MySQL.
func sqlInt(v any) (int64, bool) {
	switch t := v.(type) {
	case int64:
		return t, true
	case int32:
		return int64(t), true
	case int:
		return int64(t), true
	case float64:
		if t == math.Trunc(t) && math.Abs(t) < 1<<53 {
			return int64(t), true
		}
		return 0, false
	case []byte:
		return sqlInt(string(t))
	case string:
		s := strings.TrimSpace(t)
		if whole, frac, ok := strings.Cut(s, "."); ok && strings.Trim(frac, "0") == "" {
			s = whole
		}
		n, err := strconv.ParseInt(s, 10, 64)
		return n, err == nil
	case nil:
		return 0, true
	}
	return 0, false
}

// compareGroupValues orders group values: null first, numbers by value,
// anything else as text.
func compareGroupValues(a, b any) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return -1
	case b == nil:
		return 1
	}
	_, aText := a.(string)
	_, bText := b.(string)
	if fa, ok := ToFloat(a); ok && !aText && !bText {
		if fb, ok := ToFloat(b); ok {
			return cmp.Compare(fa, fb)
		}
	}
	return strings.Compare(Stringify(a), Stringify(b))
}

// isoWeekStart maps a YYYY-MM-DD day to the Monday of its ISO week.
func isoWeekStart(day any) any {
	s, ok := day.(string)
	if !ok || len(s) < 10 {
		return day
	}
	t, err := time.Parse("2006-01-02", s[:10])
	if err != nil {
		return day
	}
	return t.AddDate(0, 0, -((int(t.Weekday()) + 6) % 7)).Format("2006-01-02")
}
