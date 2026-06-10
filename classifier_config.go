package laozi

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// ============================================================================
// Domain config file loader
//
// Loads a domain spec from a YAML file so domains can be defined without
// recompiling. To stay dependency-free (and because Laozi is a plugin), this
// parses a documented, intentionally-small SUBSET of YAML — enough for domain
// definitions, not arbitrary YAML. For full YAML, unmarshal into []Domain with
// your own library instead; the struct tags are already in place.
//
// Supported schema (see domains.example.yaml):
//
//	fallback: general
//	domains:
//	  - name: financial_analysis
//	    description: Expenses, revenue, cash flow, margins
//	    keywords: [expense, revenue, "cash flow", profit, margin]
//	    categories: [liquidity, profitability]
//	    system_prompt: Analyze financial metrics conservatively.
//	    actions: [whitelist, reclassify, confirm]
//	    max_tokens: 800
//
// Rules: 2-space-style indentation, list items begin with "- name:", scalars may
// be quoted or bare, lists use inline [a, b, "c d"] form, "#" lines and blank
// lines are ignored. Inline trailing comments are not supported.
// ============================================================================

// DomainSpec is the parsed config: a fallback name plus the domain list.
type DomainSpec struct {
	Fallback string   `json:"fallback" yaml:"fallback"`
	Domains  []Domain `json:"domains"  yaml:"domains"`
}

// LoadDomainsFile reads and parses a domain spec from a file path.
func LoadDomainsFile(path string) (DomainSpec, error) {
	f, err := os.Open(path)
	if err != nil {
		return DomainSpec{}, err
	}
	defer f.Close()
	return LoadDomains(f)
}

// LoadDomains parses a domain spec from a reader (the documented YAML subset).
func LoadDomains(r io.Reader) (DomainSpec, error) {
	var spec DomainSpec
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	inDomains := false
	lineNo := 0
	for sc.Scan() {
		lineNo++
		raw := sc.Text()
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(raw) - len(strings.TrimLeft(raw, " \t"))

		// Top-level keys (no indentation).
		if indent == 0 {
			key, val := splitKV(trimmed)
			switch key {
			case "fallback":
				spec.Fallback = unquote(val)
			case "domains":
				inDomains = true
			default:
				return spec, fmt.Errorf("line %d: unknown top-level key %q", lineNo, key)
			}
			continue
		}

		if !inDomains {
			return spec, fmt.Errorf("line %d: indented content before 'domains:'", lineNo)
		}

		if strings.HasPrefix(trimmed, "- ") {
			spec.Domains = append(spec.Domains, Domain{})
			rest := strings.TrimSpace(trimmed[2:])
			if rest != "" {
				if err := applyField(&spec.Domains[len(spec.Domains)-1], rest, lineNo); err != nil {
					return spec, err
				}
			}
			continue
		}

		if len(spec.Domains) == 0 {
			return spec, fmt.Errorf("line %d: field outside any domain item", lineNo)
		}
		if err := applyField(&spec.Domains[len(spec.Domains)-1], trimmed, lineNo); err != nil {
			return spec, err
		}
	}
	if err := sc.Err(); err != nil {
		return spec, err
	}
	if spec.Fallback == "" {
		spec.Fallback = "general"
	}
	return spec, nil
}

func splitKV(s string) (key, val string) {
	if i := strings.Index(s, ":"); i >= 0 {
		return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+1:])
	}
	return strings.TrimSpace(s), ""
}

func applyField(d *Domain, line string, lineNo int) error {
	key, val := splitKV(line)
	switch key {
	case "name":
		d.Name = unquote(val)
	case "description":
		d.Description = unquote(val)
	case "system_prompt":
		d.SystemPrompt = unquote(val)
	case "max_tokens":
		n, err := strconv.Atoi(strings.TrimSpace(val))
		if err != nil {
			return fmt.Errorf("line %d: max_tokens must be an integer, got %q", lineNo, val)
		}
		d.MaxTokens = n
	case "keywords":
		list, err := parseList(val, lineNo)
		if err != nil {
			return err
		}
		d.Keywords = list
	case "categories":
		list, err := parseList(val, lineNo)
		if err != nil {
			return err
		}
		d.Categories = list
	case "actions":
		list, err := parseList(val, lineNo)
		if err != nil {
			return err
		}
		d.Actions = list
	default:
		return fmt.Errorf("line %d: unknown domain field %q", lineNo, key)
	}
	return nil
}

// parseList parses an inline flow list: [a, b, "c d"].
func parseList(val string, lineNo int) ([]string, error) {
	val = strings.TrimSpace(val)
	if !strings.HasPrefix(val, "[") || !strings.HasSuffix(val, "]") {
		return nil, fmt.Errorf("line %d: expected an inline list like [a, b], got %q", lineNo, val)
	}
	inner := strings.TrimSpace(val[1 : len(val)-1])
	if inner == "" {
		return nil, nil
	}
	var out []string
	for _, part := range strings.Split(inner, ",") {
		if s := unquote(strings.TrimSpace(part)); s != "" {
			out = append(out, s)
		}
	}
	return out, nil
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
