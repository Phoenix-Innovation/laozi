package laozi

import (
	"strings"
	"testing"
)

// TestDSLAllFunctions is the conformance suite for the Laozi Expression
// Language. Every function and keyword defined in the DSL reference gets:
//   - a canonical VALID usage that must parse, validate clean, and (where
//     given) compile to SQL containing the listed substrings; and
//   - where applicable an INVALID usage that must surface a specific error.
//
// One test, one row per spec construct, so coverage maps 1:1 to the reference.
func TestDSLAllFunctions(t *testing.T) {
	type fnCase struct {
		name        string   // spec construct
		valid       string   // expression that must validate clean
		sqlContains []string // substrings required in the compiled SQL
		invalid     string   // expression that must be rejected
		errContains string   // substring required in the rejection
	}

	cases := []fnCase{
		// ---- Aggregation ----
		{name: "SUM", valid: `SUM(amount)`, sqlContains: []string{"SUM(amount)"},
			invalid: `SUM(a, b)`, errContains: "SUM expects 1 argument"},
		{name: "AVG", valid: `AVG(amount)`, sqlContains: []string{"AVG(amount)"},
			invalid: `AVG(a, b)`, errContains: "AVG expects 1 argument"},
		{name: "COUNT_star", valid: `COUNT(*)`, sqlContains: []string{"COUNT(*)"},
			invalid: `COUNT(a, b)`, errContains: "COUNT expects exactly 1"},
		{name: "COUNT_field", valid: `COUNT(id)`, sqlContains: []string{"COUNT(id)"}},
		{name: "MIN", valid: `MIN(amount)`, sqlContains: []string{"MIN(amount)"},
			invalid: `MIN()`, errContains: "MIN expects 1 argument"},
		{name: "MAX", valid: `MAX(amount)`, sqlContains: []string{"MAX(amount)"},
			invalid: `MAX(a, b)`, errContains: "MAX expects 1 argument"},

		// ---- Math ----
		{name: "ROUND", valid: `ROUND(ratio, 2)`, sqlContains: []string{"ROUND(ratio, 2)"},
			invalid: `ROUND(x)`, errContains: "ROUND expects 2"},
		{name: "ABS", valid: `ABS(balance)`, sqlContains: []string{"ABS(balance)"},
			invalid: `ABS(a, b)`, errContains: "ABS expects 1"},
		{name: "NULLIF", valid: `NULLIF(total, 0)`, sqlContains: []string{"NULLIF(total, 0)"},
			invalid: `NULLIF(x)`, errContains: "NULLIF expects 2"},
		{name: "SQRT", valid: `SQRT(variance)`, sqlContains: []string{"SQRT(variance)"},
			invalid: `SQRT(a, b)`, errContains: "SQRT expects 1"},

		// ---- Statistical / Forensic ----
		{name: "GINI", valid: `GINI(amount GROUP_BY(payee))`, sqlContains: []string{"gini(amount)", "GROUP BY payee"},
			invalid: `GINI(amount)`, errContains: "GINI requires GROUP_BY"},
		{name: "STDEV", valid: `STDEV(amount)`, sqlContains: []string{"STDDEV(amount)"},
			invalid: `STDEV(a, b)`, errContains: "STDEV expects 1"},
		{name: "CHANGE", valid: `CHANGE(revenue, 3 months)`, sqlContains: []string{"change(revenue", "'3 months'"},
			invalid: `CHANGE(revenue)`, errContains: "CHANGE needs a period"},
		{name: "BENFORD", valid: `BENFORD(amount)`, sqlContains: []string{"benford(amount)"},
			invalid: `BENFORD(a, b)`, errContains: "BENFORD expects 1"},

		// ---- Time & Period ----
		{name: "OVER_days", valid: `SUM(amount) OVER(30 days)`, sqlContains: []string{"INTERVAL '30 days'"},
			invalid: `SUM(amount) OVER(5 lightyears)`, errContains: "unknown time unit"},
		{name: "OVER_weeks", valid: `SUM(amount) OVER(2 weeks)`, sqlContains: []string{"INTERVAL '2 weeks'"}},
		{name: "OVER_months", valid: `SUM(amount) OVER(6 months)`, sqlContains: []string{"INTERVAL '6 months'"}},
		{name: "OVER_year_singular", valid: `SUM(amount) OVER(1 year)`, sqlContains: []string{"INTERVAL '1 year'"}},
		{name: "PERIOD", valid: `COUNT(*) PERIOD(YTD)`, sqlContains: []string{"date_trunc('year'"},
			invalid: `COUNT(*) PERIOD(FOREVER)`, errContains: "unknown period"},
		{name: "ON", valid: `SUM(amount) WHERE(ON(weekends))`, sqlContains: []string{"ON(weekends)"},
			invalid: `SUM(amount) WHERE(ON())`, errContains: "ON expects 1 argument"},

		// ---- Named periods ----
		{name: "period_YTD", valid: `COUNT(*) PERIOD(YTD)`, sqlContains: []string{"date_trunc('year'"}},
		{name: "period_MTD", valid: `COUNT(*) PERIOD(MTD)`, sqlContains: []string{"date_trunc('month'"}},
		{name: "period_QTD", valid: `COUNT(*) PERIOD(QTD)`, sqlContains: []string{"date_trunc('quarter'"}},
		{name: "period_last_year", valid: `SUM(amount) OVER(last_year)`, sqlContains: []string{"INTERVAL '1 year'"}},
		{name: "period_last_quarter", valid: `SUM(amount) OVER(last_quarter)`, sqlContains: []string{"INTERVAL '3 months'"}},
		{name: "period_last_month", valid: `SUM(amount) OVER(last_month)`, sqlContains: []string{"INTERVAL '1 month'"}},

		// ---- Conditional & Filter ----
		{name: "WHERE", valid: `SUM(amount) WHERE(type = 'income')`, sqlContains: []string{"CASE WHEN type = 'income'"},
			invalid: `WHERE(type = 'income')`, errContains: "must follow"},
		{name: "AND", valid: `SUM(amount) WHERE(amount > 100 AND type = 'x')`, sqlContains: []string{"AND"}},
		{name: "OR", valid: `SUM(amount) WHERE(amount > 100 OR amount < 0)`, sqlContains: []string{"OR"}},
		{name: "GROUP_BY", valid: `SUM(amount GROUP_BY(region))`, sqlContains: []string{"GROUP BY region"}},

		// ---- Operators (PEMDAS) ----
		{name: "exponent", valid: `value ^ 2`, sqlContains: []string{"power(value, 2)"}},
		{name: "arithmetic", valid: `income - expenses`, sqlContains: []string{"(income - expenses)"}},

		// ---- Full composed example (conditional aggregation) ----
		{name: "ratio_integration",
			valid:       `ROUND((SUM(amount) WHERE(type = 'income') OVER(30 days)) / NULLIF(SUM(amount) WHERE(type = 'expense') OVER(30 days), 0) * 100, 2)`,
			sqlContains: []string{"SUM(CASE WHEN type = 'income'", "INTERVAL '30 days'", "NULLIF(", "ROUND("}},
	}

	seen := map[string]bool{}
	for _, c := range cases {
		if seen[c.name] {
			t.Fatalf("duplicate case name %q", c.name)
		}
		seen[c.name] = true

		t.Run(c.name, func(t *testing.T) {
			if c.valid != "" {
				if errs := CheckDSL(c.valid); len(errs) != 0 {
					t.Errorf("VALID %q was rejected: %v", c.valid, errs)
				}
				if _, err := ParseDSL(c.valid); err != nil {
					t.Errorf("ParseDSL(%q) failed: %v", c.valid, err)
				}
				if len(c.sqlContains) > 0 {
					sql, err := CompileSQL(c.valid)
					if err != nil {
						t.Errorf("CompileSQL(%q) errored: %v", c.valid, err)
					} else {
						for _, w := range c.sqlContains {
							if !strings.Contains(sql, w) {
								t.Errorf("SQL for %q missing %q\n  got: %s", c.valid, w, sql)
							}
						}
					}
				}
			}
			if c.invalid != "" {
				errs := CheckDSL(c.invalid)
				if len(errs) == 0 {
					t.Errorf("INVALID %q was accepted", c.invalid)
					return
				}
				if c.errContains != "" {
					found := false
					for _, e := range errs {
						if strings.Contains(e.Msg, c.errContains) {
							found = true
						}
					}
					if !found {
						t.Errorf("INVALID %q: want error containing %q, got %v", c.invalid, c.errContains, errs)
					}
				}
			}
		})
	}

	// Coverage guard: assert every registered function name appears in the table,
	// so a future function added to the registry can't silently go untested.
	for fn := range fnArity {
		if !seen[fn] && !seen[fn+"_star"] && !seen[fn+"_field"] {
			t.Errorf("registered function %q has no conformance case", fn)
		}
	}
}
