package laozi

import (
	"fmt"
	"strconv"
	"strings"
)

// ============================================================================
// Laozi Expression Language (DSL)
//
// A small, deterministic expression language. It lexes and parses to an AST,
// validates syntax and semantics, and compiles to SQL. The validator is
// exposed as CheckDSL — the "test parser" a host app calls to surface syntax
// or semantic errors to a user before anything is committed.
//
// The engine never executes SQL itself. The host runs the compiled SQL against
// its own datastore and feeds the numeric result back as the metric value, so
// the LLM only ever narrates a pre-computed number — never the expression.
// ============================================================================

// DSLError is a positioned parse/validation error.
type DSLError struct {
	Pos int // byte offset into the source expression
	Msg string
}

func (e DSLError) Error() string { return fmt.Sprintf("col %d: %s", e.Pos, e.Msg) }

// ---- Function registry -----------------------------------------------------

// fnArity maps function name -> required argument count. -1 means the function
// is parsed specially (COUNT, GINI, CHANGE) and arity is checked there.
var fnArity = map[string]int{
	"SUM": 1, "AVG": 1, "MIN": 1, "MAX": 1,
	"ROUND": 2, "ABS": 1, "NULLIF": 2, "SQRT": 1,
	"STDEV": 1, "BENFORD": 1, "ON": 1,
	"COUNT": -1, "GINI": -1, "CHANGE": -1,
}

// aggregateFns require a numeric field argument.
var aggregateFns = map[string]bool{
	"SUM": true, "AVG": true, "MIN": true, "MAX": true,
	"STDEV": true, "GINI": true, "BENFORD": true, "CHANGE": true,
}

var namedPeriods = map[string]bool{
	"YTD": true, "MTD": true, "QTD": true,
	"last_year": true, "last_quarter": true, "last_month": true,
}

var windowUnits = map[string]bool{
	"day": true, "days": true, "week": true, "weeks": true,
	"month": true, "months": true, "year": true, "years": true,
}

func isModifierKeyword(up string) bool {
	return up == "WHERE" || up == "OVER" || up == "PERIOD"
}

// ---- AST -------------------------------------------------------------------

type Expr interface{ isExpr() }

type NumLit struct{ V float64 }
type StrLit struct{ V string }
type NullLit struct{}
type FieldRef struct{ Name string }
type StarRef struct{}
type UnaryExpr struct {
	Op string
	X  Expr
}
type BinExpr struct {
	Op   string // + - * / ^
	L, R Expr
}
type CmpExpr struct {
	Op   string // = != < > <= >=
	L, R Expr
}
type LogicExpr struct {
	Op   string // AND | OR
	L, R Expr
}
type FuncExpr struct {
	Name    string
	Args    []Expr
	GroupBy string // GINI(amount GROUP_BY(payee))
}
type DurationExpr struct {
	N    int
	Unit string
}
type OverSpec struct {
	N      int
	Unit   string
	Period string // set instead of N/Unit when OVER(last_year)
}
type ModExpr struct {
	Base   Expr
	Where  Expr
	Over   *OverSpec
	Period string
}

func (NumLit) isExpr()       {}
func (StrLit) isExpr()       {}
func (NullLit) isExpr()      {}
func (FieldRef) isExpr()     {}
func (StarRef) isExpr()      {}
func (UnaryExpr) isExpr()    {}
func (BinExpr) isExpr()      {}
func (CmpExpr) isExpr()      {}
func (LogicExpr) isExpr()    {}
func (FuncExpr) isExpr()     {}
func (DurationExpr) isExpr() {}
func (*ModExpr) isExpr()     {}

// ---- Lexer -----------------------------------------------------------------

type tokKind int

const (
	tEOF tokKind = iota
	tNumber
	tString
	tIdent
	tLParen
	tRParen
	tComma
	tStar
	tPlus
	tMinus
	tSlash
	tCaret
	tEq
	tNeq
	tLt
	tGt
	tLte
	tGte
)

type token struct {
	kind tokKind
	text string
	pos  int
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
func isIdentPart(c byte) bool { return isIdentStart(c) || (c >= '0' && c <= '9') }

func lexAll(src string) ([]token, *DSLError) {
	var toks []token
	i := 0
	for i < len(src) {
		c := src[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			i++
			continue
		}
		start := i
		switch {
		case c == '(':
			toks = append(toks, token{tLParen, "(", start})
			i++
		case c == ')':
			toks = append(toks, token{tRParen, ")", start})
			i++
		case c == ',':
			toks = append(toks, token{tComma, ",", start})
			i++
		case c == '*':
			toks = append(toks, token{tStar, "*", start})
			i++
		case c == '+':
			toks = append(toks, token{tPlus, "+", start})
			i++
		case c == '-':
			toks = append(toks, token{tMinus, "-", start})
			i++
		case c == '/':
			toks = append(toks, token{tSlash, "/", start})
			i++
		case c == '^':
			toks = append(toks, token{tCaret, "^", start})
			i++
		case c == '=':
			toks = append(toks, token{tEq, "=", start})
			i++
		case c == '!':
			if i+1 < len(src) && src[i+1] == '=' {
				toks = append(toks, token{tNeq, "!=", start})
				i += 2
			} else {
				return nil, &DSLError{start, "unexpected '!' (did you mean '!='?)"}
			}
		case c == '<':
			if i+1 < len(src) && src[i+1] == '=' {
				toks = append(toks, token{tLte, "<=", start})
				i += 2
			} else {
				toks = append(toks, token{tLt, "<", start})
				i++
			}
		case c == '>':
			if i+1 < len(src) && src[i+1] == '=' {
				toks = append(toks, token{tGte, ">=", start})
				i += 2
			} else {
				toks = append(toks, token{tGt, ">", start})
				i++
			}
		case c == '\'':
			i++
			var sb strings.Builder
			for i < len(src) && src[i] != '\'' {
				sb.WriteByte(src[i])
				i++
			}
			if i >= len(src) {
				return nil, &DSLError{start, "unterminated string literal"}
			}
			i++ // closing quote
			toks = append(toks, token{tString, sb.String(), start})
		case c >= '0' && c <= '9' || (c == '.' && i+1 < len(src) && src[i+1] >= '0' && src[i+1] <= '9'):
			j := i
			dots := 0
			for j < len(src) && (src[j] >= '0' && src[j] <= '9' || src[j] == '.') {
				if src[j] == '.' {
					dots++
				}
				j++
			}
			if dots > 1 {
				return nil, &DSLError{start, "malformed number literal"}
			}
			toks = append(toks, token{tNumber, src[i:j], start})
			i = j
		case isIdentStart(c):
			j := i
			for j < len(src) && isIdentPart(src[j]) {
				j++
			}
			toks = append(toks, token{tIdent, src[i:j], start})
			i = j
		default:
			return nil, &DSLError{start, fmt.Sprintf("unexpected character %q", string(c))}
		}
	}
	toks = append(toks, token{tEOF, "", len(src)})
	return toks, nil
}

// ---- Parser ----------------------------------------------------------------

type parser struct {
	toks []token
	pos  int
}

func (p *parser) peek() token { return p.toks[p.pos] }
func (p *parser) advance() token {
	t := p.toks[p.pos]
	if p.pos < len(p.toks)-1 {
		p.pos++
	}
	return t
}
func (p *parser) expect(k tokKind, what string) (token, *DSLError) {
	t := p.peek()
	if t.kind != k {
		return t, &DSLError{t.pos, fmt.Sprintf("expected %s", what)}
	}
	return p.advance(), nil
}

// ParseDSL parses an expression to an AST, returning the first error.
func ParseDSL(src string) (Expr, error) {
	toks, lerr := lexAll(src)
	if lerr != nil {
		return nil, *lerr
	}
	p := &parser{toks: toks}
	node, err := p.parseArith()
	if err != nil {
		return nil, *err
	}
	if p.peek().kind != tEOF {
		return nil, DSLError{p.peek().pos, fmt.Sprintf("unexpected %q after expression", p.peek().text)}
	}
	return node, nil
}

// parseArith: + - (lowest)
func (p *parser) parseArith() (Expr, *DSLError) {
	left, err := p.parseMulDiv()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == tPlus || p.peek().kind == tMinus {
		op := p.advance().text
		right, err := p.parseMulDiv()
		if err != nil {
			return nil, err
		}
		left = BinExpr{Op: op, L: left, R: right}
	}
	return left, nil
}

func (p *parser) parseMulDiv() (Expr, *DSLError) {
	left, err := p.parsePow()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == tStar || p.peek().kind == tSlash {
		op := p.advance().text
		right, err := p.parsePow()
		if err != nil {
			return nil, err
		}
		left = BinExpr{Op: op, L: left, R: right}
	}
	return left, nil
}

// parsePow: ^ is right-associative.
// parsePow parses exponentiation, which is right-associative.
//
// PRECEDENCE NOTE (C-10): unary minus binds TIGHTER than '^', because the left
// operand here is produced by parseUnary. So `-a^b` parses as `(-a)^b`, NOT the
// `-(a^b)` you may expect from some languages. This is intentional and locked by
// tests; use explicit parentheses (`-(a^b)`) when you mean the other grouping.
func (p *parser) parsePow() (Expr, *DSLError) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	if p.peek().kind == tCaret {
		p.advance()
		right, err := p.parsePow()
		if err != nil {
			return nil, err
		}
		return BinExpr{Op: "^", L: left, R: right}, nil
	}
	return left, nil
}

func (p *parser) parseUnary() (Expr, *DSLError) {
	if p.peek().kind == tMinus {
		p.advance()
		x, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return UnaryExpr{Op: "-", X: x}, nil
	}
	return p.parsePostfix()
}

// parsePostfix: primary followed by WHERE/OVER/PERIOD modifier clauses.
func (p *parser) parsePostfix() (Expr, *DSLError) {
	node, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == tIdent && isModifierKeyword(strings.ToUpper(p.peek().text)) {
		kw := strings.ToUpper(p.advance().text)
		if _, err := p.expect(tLParen, "'(' after "+kw); err != nil {
			return nil, err
		}
		mod := asMod(node)
		switch kw {
		case "WHERE":
			cond, err := p.parseBool()
			if err != nil {
				return nil, err
			}
			mod.Where = cond
		case "OVER":
			spec, err := p.parseOverSpec()
			if err != nil {
				return nil, err
			}
			mod.Over = spec
		case "PERIOD":
			pd, err := p.parsePeriodName()
			if err != nil {
				return nil, err
			}
			mod.Period = pd
		}
		if _, err := p.expect(tRParen, "')' to close "+kw); err != nil {
			return nil, err
		}
		node = mod
	}
	return node, nil
}

func asMod(node Expr) *ModExpr {
	if m, ok := node.(*ModExpr); ok {
		return m
	}
	return &ModExpr{Base: node}
}

func (p *parser) parsePrimary() (Expr, *DSLError) {
	t := p.peek()
	switch t.kind {
	case tNumber:
		p.advance()
		v, _ := strconv.ParseFloat(t.text, 64)
		return NumLit{v}, nil
	case tString:
		p.advance()
		return StrLit{t.text}, nil
	case tStar:
		p.advance()
		return StarRef{}, nil
	case tLParen:
		p.advance()
		inner, err := p.parseArith()
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(tRParen, "')'"); err != nil {
			return nil, err
		}
		return inner, nil
	case tIdent:
		up := strings.ToUpper(t.text)
		if up == "NULL" {
			p.advance()
			return NullLit{}, nil
		}
		if isModifierKeyword(up) {
			return nil, &DSLError{t.pos, fmt.Sprintf("%s clause must follow an expression", up)}
		}
		// function call vs bare field
		if p.toks[p.pos+1].kind == tLParen {
			return p.parseCall(t)
		}
		p.advance()
		return FieldRef{t.text}, nil
	default:
		return nil, &DSLError{t.pos, fmt.Sprintf("unexpected %q", t.text)}
	}
}

func (p *parser) parseCall(name token) (Expr, *DSLError) {
	up := strings.ToUpper(name.text)
	p.advance() // name
	if _, err := p.expect(tLParen, "'('"); err != nil {
		return nil, err
	}
	fn := FuncExpr{Name: up}

	switch up {
	case "CHANGE":
		field, err := p.parseArith()
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(tComma, "',' (CHANGE needs a period: CHANGE(field, 3 months))"); err != nil {
			return nil, err
		}
		dur, err := p.parseDuration()
		if err != nil {
			return nil, err
		}
		fn.Args = []Expr{field, dur}
	default:
		// general comma-separated args; first arg may carry a trailing GROUP_BY
		if p.peek().kind != tRParen {
			for {
				a, err := p.parseArith()
				if err != nil {
					return nil, err
				}
				fn.Args = append(fn.Args, a)
				if p.peek().kind == tIdent && strings.ToUpper(p.peek().text) == "GROUP_BY" {
					p.advance()
					if _, err := p.expect(tLParen, "'(' after GROUP_BY"); err != nil {
						return nil, err
					}
					key, err := p.expect(tIdent, "a field name in GROUP_BY")
					if err != nil {
						return nil, err
					}
					fn.GroupBy = key.text
					if _, err := p.expect(tRParen, "')' to close GROUP_BY"); err != nil {
						return nil, err
					}
				}
				if p.peek().kind != tComma {
					break
				}
				p.advance()
			}
		}
	}
	if _, err := p.expect(tRParen, "')' to close "+up+"()"); err != nil {
		return nil, err
	}
	return fn, nil
}

func (p *parser) parseDuration() (Expr, *DSLError) {
	if p.peek().kind == tNumber {
		nTok := p.advance()
		n, _ := strconv.Atoi(strings.Split(nTok.text, ".")[0])
		unitTok, err := p.expect(tIdent, "a time unit (days/weeks/months/years)")
		if err != nil {
			return nil, err
		}
		if !windowUnits[strings.ToLower(unitTok.text)] {
			return nil, &DSLError{unitTok.pos, fmt.Sprintf("unknown time unit %q", unitTok.text)}
		}
		return DurationExpr{N: n, Unit: strings.ToLower(unitTok.text)}, nil
	}
	// named period as a duration
	pd, err := p.parsePeriodName()
	if err != nil {
		return nil, err
	}
	return DurationExpr{Unit: pd}, nil
}

func (p *parser) parseOverSpec() (*OverSpec, *DSLError) {
	if p.peek().kind == tNumber {
		nTok := p.advance()
		n, _ := strconv.Atoi(strings.Split(nTok.text, ".")[0])
		unitTok, err := p.expect(tIdent, "a time unit (days/weeks/months/years)")
		if err != nil {
			return nil, err
		}
		if !windowUnits[strings.ToLower(unitTok.text)] {
			return nil, &DSLError{unitTok.pos, fmt.Sprintf("unknown time unit %q", unitTok.text)}
		}
		return &OverSpec{N: n, Unit: strings.ToLower(unitTok.text)}, nil
	}
	pd, err := p.parsePeriodName()
	if err != nil {
		return nil, err
	}
	return &OverSpec{Period: pd}, nil
}

func (p *parser) parsePeriodName() (string, *DSLError) {
	t, err := p.expect(tIdent, "a named period (YTD, MTD, QTD, last_year, ...)")
	if err != nil {
		return "", err
	}
	if !namedPeriods[t.text] {
		return "", &DSLError{t.pos, fmt.Sprintf("unknown period %q", t.text)}
	}
	return t.text, nil
}

// parseBool: boolean expression for WHERE(...) with AND/OR and comparisons.
func (p *parser) parseBool() (Expr, *DSLError) {
	left, err := p.parseCompare()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == tIdent {
		up := strings.ToUpper(p.peek().text)
		if up != "AND" && up != "OR" {
			break
		}
		p.advance()
		right, err := p.parseCompare()
		if err != nil {
			return nil, err
		}
		left = LogicExpr{Op: up, L: left, R: right}
	}
	return left, nil
}

func (p *parser) parseCompare() (Expr, *DSLError) {
	left, err := p.parseArith()
	if err != nil {
		return nil, err
	}
	switch p.peek().kind {
	case tEq, tNeq, tLt, tGt, tLte, tGte:
		op := p.advance().text
		right, err := p.parseArith()
		if err != nil {
			return nil, err
		}
		return CmpExpr{Op: op, L: left, R: right}, nil
	}
	return left, nil // bare boolean (e.g. ON(weekends))
}

// ---- Semantic validation (the "test parser") -------------------------------

// CheckDSL parses and validates an expression, returning all errors found.
// An empty slice means the expression is valid. Host apps call this to flag
// syntax or semantic errors to the user before proposing a category.
func CheckDSL(src string) []DSLError {
	if strings.TrimSpace(src) == "" {
		return []DSLError{{0, "empty expression"}}
	}
	node, err := ParseDSL(src)
	if err != nil {
		if de, ok := err.(DSLError); ok {
			return []DSLError{de}
		}
		return []DSLError{{0, err.Error()}}
	}
	var errs []DSLError
	semCheck(node, &errs)
	return errs
}

func semCheck(n Expr, errs *[]DSLError) {
	switch x := n.(type) {
	case UnaryExpr:
		semCheck(x.X, errs)
	case BinExpr:
		semCheck(x.L, errs)
		semCheck(x.R, errs)
	case CmpExpr:
		semCheck(x.L, errs)
		semCheck(x.R, errs)
	case LogicExpr:
		semCheck(x.L, errs)
		semCheck(x.R, errs)
	case *ModExpr:
		semCheck(x.Base, errs)
		if x.Where != nil {
			semCheck(x.Where, errs)
		}
	case FuncExpr:
		arity, known := fnArity[x.Name]
		if !known {
			*errs = append(*errs, DSLError{0, fmt.Sprintf("unknown function %s", x.Name)})
			return
		}
		switch x.Name {
		case "COUNT":
			if len(x.Args) != 1 {
				*errs = append(*errs, DSLError{0, "COUNT expects exactly 1 argument (a field or *)"})
			}
		case "GINI":
			if len(x.Args) != 1 {
				*errs = append(*errs, DSLError{0, "GINI expects 1 field argument"})
			}
			if x.GroupBy == "" {
				*errs = append(*errs, DSLError{0, "GINI requires GROUP_BY(field) for concentration analysis"})
			}
		case "CHANGE":
			if len(x.Args) != 2 {
				*errs = append(*errs, DSLError{0, "CHANGE expects (field, period) e.g. CHANGE(revenue, 3 months)"})
			}
		default:
			if arity >= 0 && len(x.Args) != arity {
				*errs = append(*errs, DSLError{0, fmt.Sprintf("%s expects %d argument(s), got %d", x.Name, arity, len(x.Args))})
			}
		}
		// aggregate type check: numeric field, not a string literal
		if aggregateFns[x.Name] && len(x.Args) >= 1 {
			if _, isStr := x.Args[0].(StrLit); isStr {
				*errs = append(*errs, DSLError{0, fmt.Sprintf("%s requires a numeric field, got a string", x.Name)})
			}
		}
		for _, a := range x.Args {
			semCheck(a, errs)
		}
	}
}

// ---- SQL compiler ----------------------------------------------------------

// CompileSQL parses, validates, and compiles an expression to a SQL string.
//
// Conventions (since the plugin has no schema): time windows and periods filter
// a column named event_time; the forensic/statistical functions GINI, BENFORD,
// CHANGE compile to lowercase calls the host must provide as SQL UDFs. WHERE and
// time filters attached to an aggregate compile to conditional aggregation
// (AGG(CASE WHEN <pred> THEN <field> END)) so composed arithmetic stays scalar.
func CompileSQL(src string) (string, error) {
	if errs := CheckDSL(src); len(errs) > 0 {
		return "", errs[0]
	}
	node, _ := ParseDSL(src)
	return compileNode(node), nil
}

// sqlEscapeLiteral makes a string safe to place inside single quotes in
// generated SQL: single quotes are doubled (standard SQL escaping) and NUL
// bytes are dropped. (C-10)
func sqlEscapeLiteral(s string) string {
	s = strings.ReplaceAll(s, "\x00", "")
	return strings.ReplaceAll(s, "'", "''")
}

// sqlSafeIdent returns name unchanged when it is a plain identifier
// ([A-Za-z_][A-Za-z0-9_]*); otherwise it returns a double-quoted, escaped
// identifier so a non-conforming name cannot break out of identifier position.
// (C-10)
func sqlSafeIdent(name string) string {
	if isSafeIdent(name) {
		return name
	}
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// isSafeIdent reports whether name is a bare SQL identifier.
func isSafeIdent(name string) bool {
	if name == "" || !isIdentStart(name[0]) {
		return false
	}
	for i := 1; i < len(name); i++ {
		if !isIdentPart(name[i]) {
			return false
		}
	}
	return true
}

func compileNode(n Expr) string {
	switch x := n.(type) {
	case NumLit:
		return strconv.FormatFloat(x.V, 'g', -1, 64)
	case StrLit:
		return "'" + sqlEscapeLiteral(x.V) + "'"
	case NullLit:
		return "NULL"
	case FieldRef:
		// FieldRef.Name is produced only by the identifier lexer
		// ([A-Za-z_][A-Za-z0-9_]*), so it is a safe SQL identifier by
		// construction. sqlSafeIdent re-checks defensively in case an AST is
		// built programmatically.
		return sqlSafeIdent(x.Name)
	case StarRef:
		return "*"
	case UnaryExpr:
		return "(-" + compileNode(x.X) + ")"
	case BinExpr:
		if x.Op == "^" {
			return "power(" + compileNode(x.L) + ", " + compileNode(x.R) + ")"
		}
		return "(" + compileNode(x.L) + " " + x.Op + " " + compileNode(x.R) + ")"
	case CmpExpr:
		return compileNode(x.L) + " " + x.Op + " " + compileNode(x.R)
	case LogicExpr:
		return compileNode(x.L) + " " + x.Op + " " + compileNode(x.R)
	case DurationExpr:
		if x.Unit != "" && x.N == 0 {
			return "'" + sqlEscapeLiteral(x.Unit) + "'"
		}
		return fmt.Sprintf("'%d %s'", x.N, sqlEscapeLiteral(x.Unit))
	case FuncExpr:
		return compileFunc(x, "")
	case *ModExpr:
		return compileMod(x)
	}
	return ""
}

func compileFunc(fn FuncExpr, extraPred string) string {
	name := fn.Name
	switch name {
	case "STDEV":
		name = "STDDEV"
	case "GINI", "BENFORD", "CHANGE":
		name = strings.ToLower(name) // host-provided UDF
	}

	// Conditional aggregation: fold a predicate into the aggregate.
	if extraPred != "" && aggregateFns[fn.Name] && len(fn.Args) >= 1 {
		field := compileNode(fn.Args[0])
		inner := fmt.Sprintf("CASE WHEN %s THEN %s END", extraPred, field)
		out := name + "(" + inner + ")"
		if fn.GroupBy != "" {
			out += " /* GROUP BY " + fn.GroupBy + " */"
		}
		return out
	}
	if fn.Name == "COUNT" && extraPred != "" {
		return "COUNT(CASE WHEN " + extraPred + " THEN 1 END)"
	}

	var args []string
	for _, a := range fn.Args {
		args = append(args, compileNode(a))
	}
	out := name + "(" + strings.Join(args, ", ") + ")"
	if fn.GroupBy != "" {
		out += " /* GROUP BY " + fn.GroupBy + " */"
	}
	return out
}

func compileMod(m *ModExpr) string {
	var preds []string
	if m.Where != nil {
		preds = append(preds, compileNode(m.Where))
	}
	if m.Over != nil {
		preds = append(preds, windowPredicate(m.Over))
	}
	if m.Period != "" {
		preds = append(preds, periodPredicate(m.Period))
	}
	pred := strings.Join(preds, " AND ")

	if fn, ok := m.Base.(FuncExpr); ok {
		return compileFunc(fn, pred)
	}
	// Non-aggregate base: emit base with a trailing predicate comment.
	base := compileNode(m.Base)
	if pred != "" {
		return base + " /* WHERE " + pred + " */"
	}
	return base
}

func windowPredicate(o *OverSpec) string {
	if o.Period != "" {
		return periodPredicate(o.Period)
	}
	return fmt.Sprintf("event_time >= NOW() - INTERVAL '%d %s'", o.N, o.Unit)
}

func periodPredicate(p string) string {
	switch p {
	case "YTD":
		return "event_time >= date_trunc('year', NOW())"
	case "MTD":
		return "event_time >= date_trunc('month', NOW())"
	case "QTD":
		return "event_time >= date_trunc('quarter', NOW())"
	case "last_year":
		return "event_time >= date_trunc('year', NOW()) - INTERVAL '1 year' AND event_time < date_trunc('year', NOW())"
	case "last_quarter":
		return "event_time >= date_trunc('quarter', NOW()) - INTERVAL '3 months' AND event_time < date_trunc('quarter', NOW())"
	case "last_month":
		return "event_time >= date_trunc('month', NOW()) - INTERVAL '1 month' AND event_time < date_trunc('month', NOW())"
	}
	return "TRUE"
}
