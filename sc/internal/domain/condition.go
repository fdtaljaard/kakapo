package domain

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The archive fix's condition (docs/phase5a-contract.md, "The condition"): an optional WHERE fragment
// beside the date column ("Status = 'Closed'"), so "closed orders older than a year" can be said. It
// is the one piece of user text that reaches a statement, so it is checked word by word against an
// allow list before anything is built from it, and what is built is the canonical form: every column
// name as the catalogue spells it, quoted. Anything the list does not know fails.

// maxConditionLength is the longest condition accepted, in characters.
const maxConditionLength = 1000

// The words a condition may use. conditionKeywords are the logic and comparison words;
// conditionFunctions the functions (followed by a parenthesis); conditionDateParts the first
// argument of DATEADD and DATEDIFF; conditionTypes the type of CAST (after AS) and CONVERT (first
// argument).
var (
	conditionKeywords  = wordSet("AND", "OR", "NOT", "IN", "LIKE", "IS", "NULL", "BETWEEN")
	conditionFunctions = wordSet("GETDATE", "SYSDATETIME", "DATEADD", "DATEDIFF", "CONVERT", "CAST")
	conditionDateParts = wordSet("YEAR", "YY", "YYYY", "QUARTER", "QQ", "Q", "MONTH", "MM", "M", "DAYOFYEAR", "DY", "Y",
		"DAY", "DD", "D", "WEEK", "WK", "WW", "HOUR", "HH", "MINUTE", "MI", "N", "SECOND", "SS", "S")
	conditionTypes = wordSet("DATE", "DATETIME", "DATETIME2", "SMALLDATETIME", "TIME", "INT", "BIGINT", "SMALLINT",
		"TINYINT", "BIT", "DECIMAL", "NUMERIC", "FLOAT", "REAL", "MONEY", "VARCHAR", "NVARCHAR", "CHAR", "NCHAR")
	// conditionRefused are words that are refused bare even when a column has that name (a column
	// named Select is written [Select]): the statements and clauses a subquery or a second statement
	// needs.
	conditionRefused = wordSet("SELECT", "EXEC", "EXECUTE", "INSERT", "UPDATE", "DELETE", "DROP", "ALTER", "CREATE",
		"TRUNCATE", "MERGE", "UNION", "FROM", "WHERE", "INTO", "GRANT", "REVOKE", "DENY", "DECLARE", "SET", "EXISTS",
		"WAITFOR", "SHUTDOWN", "OPENROWSET", "OPENQUERY", "OPENDATASOURCE", "BACKUP", "RESTORE", "KILL", "GO", "USE")
)

func wordSet(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

// condition token kinds.
const (
	tokWord   = iota // a bare word: keyword, function, date part, type or column
	tokQuoted        // a quoted name: [x] or "x"
	tokString        // 'x' or N'x'
	tokNumber        // 12, 1.5
	tokOp            // = <> != < > <= >= + - * /
	tokOpen          // (
	tokClose         // )
	tokComma         // ,
)

type condToken struct {
	kind int
	text string // the word, the unquoted name, the literal as typed, the operator
}

// ValidateCondition checks a condition against the table's column names (as the catalogue read
// them): it may use those columns (bare or quoted with [ ] or " "), comparison operators, AND OR
// NOT IN LIKE IS NULL BETWEEN, string and number literals, parentheses, and the functions GETDATE,
// SYSDATETIME, DATEADD, DATEDIFF, CONVERT and CAST. Semicolons, comments, variables (@), subqueries,
// the xp_ and sp_ procedures and unknown names fail, with the reason. An empty condition is valid
// (no condition).
func ValidateCondition(cond string, columns []string) error {
	_, err := CanonicalCondition(cond, columns)
	return err
}

// CanonicalCondition is ValidateCondition that also returns the condition as kakapo writes it into
// a statement: column names as the catalogue spells them and quoted, keywords and functions in
// capitals, literals as typed, one space between words. "" for an empty condition.
func CanonicalCondition(cond string, columns []string) (string, error) {
	cond = strings.TrimSpace(cond)
	if cond == "" {
		return "", nil
	}
	if utf8.RuneCountInString(cond) > maxConditionLength {
		return "", fmt.Errorf("the condition is longer than %d characters", maxConditionLength)
	}
	// Refused anywhere, even inside quotes: the condition is also carried in the job step's
	// options comment, which a comment marker would end.
	for _, bad := range []struct{ s, why string }{
		{";", "a semicolon (a second statement)"},
		{"--", "a comment (--)"},
		{"/*", "a comment (/*)"},
		{"*/", "a comment marker (*/)"},
		{"@", "a variable (@)"},
	} {
		if strings.Contains(cond, bad.s) {
			return "", fmt.Errorf("the condition may not contain %s", bad.why)
		}
	}
	for _, r := range cond {
		if unicode.IsControl(r) && r != '\t' && r != '\n' && r != '\r' {
			return "", errors.New("the condition contains a control character")
		}
	}
	toks, err := tokenizeCondition(cond)
	if err != nil {
		return "", err
	}
	byName := map[string]string{}
	for _, c := range columns {
		byName[strings.ToLower(c)] = c
	}
	out := make([]string, 0, len(toks))
	depth := 0
	for i, t := range toks {
		prev := func(n int) condToken {
			if i-n < 0 {
				return condToken{kind: -1}
			}
			return toks[i-n]
		}
		switch t.kind {
		case tokOpen:
			depth++
			out = append(out, "(")
		case tokClose:
			depth--
			if depth < 0 {
				return "", errors.New("the condition closes a parenthesis it did not open")
			}
			out = append(out, ")")
		case tokComma:
			if depth == 0 {
				return "", errors.New("the condition has a comma outside a function or an IN list")
			}
			out = append(out, ",")
		case tokOp, tokString, tokNumber:
			out = append(out, t.text)
		case tokQuoted:
			c, ok := byName[strings.ToLower(t.text)]
			if !ok {
				return "", fmt.Errorf("the condition names %s, which is not a column of the table", QuoteName(t.text))
			}
			out = append(out, QuoteName(c))
		case tokWord:
			up := strings.ToUpper(t.text)
			lower := strings.ToLower(t.text)
			next := condToken{kind: -1}
			if i+1 < len(toks) {
				next = toks[i+1]
			}
			switch {
			case strings.HasPrefix(lower, "xp_") || strings.HasPrefix(lower, "sp_"):
				return "", fmt.Errorf("the condition may not call a procedure (%s)", t.text)
			case conditionRefused[up]:
				return "", fmt.Errorf("the condition may not use %s: only a comparison of the table's own columns is allowed", up)
			case conditionFunctions[up] && next.kind == tokOpen:
				out = append(out, up)
			case conditionKeywords[up]:
				out = append(out, up)
			case conditionDateParts[up] && prev(1).kind == tokOpen && prev(2).kind == tokWord &&
				(strings.EqualFold(prev(2).text, "DATEADD") || strings.EqualFold(prev(2).text, "DATEDIFF")):
				out = append(out, up)
			case conditionTypes[up] && (strings.EqualFold(prev(1).text, "AS") && prev(1).kind == tokWord ||
				prev(1).kind == tokOpen && prev(2).kind == tokWord && strings.EqualFold(prev(2).text, "CONVERT")):
				out = append(out, up)
			case up == "AS" && depth > 0:
				out = append(out, up)
			default:
				c, ok := byName[lower]
				if !ok {
					return "", fmt.Errorf("the condition names %s, which is not a column of the table nor a word it may use", t.text)
				}
				out = append(out, QuoteName(c))
			}
		}
	}
	if depth != 0 {
		return "", errors.New("the condition leaves a parenthesis open")
	}
	return joinConditionTokens(out), nil
}

// joinConditionTokens writes the tokens with one space between them, none inside parentheses or
// before a comma, and none between a function and its parenthesis.
func joinConditionTokens(ts []string) string {
	var b strings.Builder
	for i, t := range ts {
		if i > 0 {
			p := ts[i-1]
			glue := t == ")" || t == "," || p == "(" || (t == "(" && conditionFunctions[p])
			if !glue {
				b.WriteByte(' ')
			}
		}
		b.WriteString(t)
	}
	return b.String()
}

// tokenizeCondition splits a condition into its tokens; a character outside the allowed set fails.
func tokenizeCondition(s string) ([]condToken, error) {
	rs := []rune(s)
	out := []condToken{}
	for i := 0; i < len(rs); {
		r := rs[i]
		switch {
		case unicode.IsSpace(r):
			i++
		case r == '\'' || ((r == 'N' || r == 'n') && i+1 < len(rs) && rs[i+1] == '\''):
			start := i
			if r != '\'' {
				i++
			}
			i++ // the opening quote
			closed := false
			for i < len(rs) {
				if rs[i] == '\'' {
					if i+1 < len(rs) && rs[i+1] == '\'' {
						i += 2
						continue
					}
					i++
					closed = true
					break
				}
				i++
			}
			if !closed {
				return nil, errors.New("the condition has a string that is not closed (a ' is missing)")
			}
			out = append(out, condToken{kind: tokString, text: string(rs[start:i])})
		case r == '[' || r == '"':
			end := ']'
			if r == '"' {
				end = '"'
			}
			i++
			var name strings.Builder
			closed := false
			for i < len(rs) {
				if rs[i] == end {
					if i+1 < len(rs) && rs[i+1] == end {
						name.WriteRune(end)
						i += 2
						continue
					}
					i++
					closed = true
					break
				}
				name.WriteRune(rs[i])
				i++
			}
			if !closed || name.Len() == 0 {
				return nil, errors.New("the condition has a quoted name that is not closed or is empty")
			}
			out = append(out, condToken{kind: tokQuoted, text: name.String()})
		case r >= '0' && r <= '9' || (r == '.' && i+1 < len(rs) && rs[i+1] >= '0' && rs[i+1] <= '9'):
			start, dot := i, false
			for i < len(rs) && (rs[i] >= '0' && rs[i] <= '9' || rs[i] == '.' && !dot) {
				if rs[i] == '.' {
					dot = true
				}
				i++
			}
			if i < len(rs) && (unicode.IsLetter(rs[i]) || rs[i] == '_') {
				return nil, fmt.Errorf("the condition has a number followed by letters (%s…)", string(rs[start:i+1]))
			}
			out = append(out, condToken{kind: tokNumber, text: string(rs[start:i])})
		case unicode.IsLetter(r) || r == '_':
			start := i
			for i < len(rs) && (unicode.IsLetter(rs[i]) || unicode.IsDigit(rs[i]) || rs[i] == '_' || rs[i] == '#' || rs[i] == '$') {
				i++
			}
			out = append(out, condToken{kind: tokWord, text: string(rs[start:i])})
		case r == '(':
			out = append(out, condToken{kind: tokOpen, text: "("})
			i++
		case r == ')':
			out = append(out, condToken{kind: tokClose, text: ")"})
			i++
		case r == ',':
			out = append(out, condToken{kind: tokComma, text: ","})
			i++
		case r == '<' || r == '>' || r == '!' || r == '=':
			op := string(r)
			if i+1 < len(rs) && (rs[i+1] == '=' || (r == '<' && rs[i+1] == '>')) {
				op += string(rs[i+1])
			}
			if op == "!" {
				return nil, errors.New("the condition has a ! that is not part of !=")
			}
			out = append(out, condToken{kind: tokOp, text: op})
			i += len(op)
		case r == '+' || r == '-' || r == '*' || r == '/':
			out = append(out, condToken{kind: tokOp, text: string(r)})
			i++
		default:
			return nil, fmt.Errorf("the condition may not contain %q", string(r))
		}
	}
	if len(out) == 0 {
		return nil, errors.New("the condition is empty")
	}
	return out, nil
}
