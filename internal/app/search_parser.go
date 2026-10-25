package app

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

type queryNode struct {
	op, value   string
	left, right *queryNode
}
type queryToken struct {
	value   string
	literal bool
}
type queryParser struct {
	tokens []queryToken
	pos    int
	depth  int
}

func lexQuery(input string) ([]queryToken, error) {
	if len(input) > 4096 || strings.IndexByte(input, 0) >= 0 {
		return nil, errors.New("search exceeds 4096 bytes or contains NUL")
	}
	out := []queryToken{}
	r := []rune(input)
	for i := 0; i < len(r); {
		if unicode.IsSpace(r[i]) {
			i++
			continue
		}
		if len(out) >= 128 {
			return nil, errors.New("search exceeds 128 tokens")
		}
		if r[i] == '(' || r[i] == ')' {
			out = append(out, queryToken{value: string(r[i])})
			i++
			continue
		}
		var b strings.Builder
		quoted := false
		literal := r[i] == '"'
		for i < len(r) {
			c := r[i]
			if !quoted && (unicode.IsSpace(c) || c == '(' || c == ')') {
				break
			}
			if c == '"' {
				quoted = !quoted
				i++
				continue
			}
			if c == '\\' && i+1 < len(r) {
				i++
				c = r[i]
			}
			b.WriteRune(c)
			i++
		}
		if quoted {
			return nil, errors.New("unterminated quote")
		}
		if b.Len() == 0 {
			return nil, errors.New("empty search term")
		}
		out = append(out, queryToken{value: b.String(), literal: literal})
	}
	return out, nil
}
func parseQuery(q string) (*queryNode, error) {
	tokens, e := lexQuery(q)
	if e != nil {
		return nil, e
	}
	if len(tokens) == 0 {
		return nil, nil
	}
	p := queryParser{tokens: tokens}
	node, e := p.or()
	if e != nil {
		return nil, e
	}
	if p.pos != len(tokens) {
		return nil, errors.New("unexpected search token")
	}
	return node, nil
}
func (p *queryParser) peek(s string) bool {
	return p.pos < len(p.tokens) && !p.tokens[p.pos].literal && strings.EqualFold(p.tokens[p.pos].value, s)
}
func (p *queryParser) or() (*queryNode, error) {
	l, e := p.and()
	if e != nil {
		return nil, e
	}
	for p.peek("OR") {
		p.pos++
		r, e := p.and()
		if e != nil {
			return nil, e
		}
		l = &queryNode{op: "OR", left: l, right: r}
	}
	return l, nil
}
func (p *queryParser) and() (*queryNode, error) {
	l, e := p.unary()
	if e != nil {
		return nil, e
	}
	for p.pos < len(p.tokens) && !p.peek("OR") && !p.peek(")") {
		if p.peek("AND") {
			p.pos++
		}
		r, e := p.unary()
		if e != nil {
			return nil, e
		}
		l = &queryNode{op: "AND", left: l, right: r}
	}
	return l, nil
}
func (p *queryParser) unary() (*queryNode, error) {
	p.depth++
	defer func() { p.depth-- }()
	if p.depth > 16 {
		return nil, errors.New("search nesting exceeds 16")
	}
	if p.peek("NOT") {
		p.pos++
		r, e := p.unary()
		return &queryNode{op: "NOT", left: r}, e
	}
	if p.peek("(") {
		p.pos++
		n, e := p.or()
		if e != nil {
			return nil, e
		}
		if !p.peek(")") {
			return nil, errors.New("missing closing parenthesis")
		}
		p.pos++
		return n, nil
	}
	if p.pos >= len(p.tokens) || p.peek(")") || p.peek("AND") || p.peek("OR") {
		return nil, errors.New("missing search operand")
	}
	token := p.tokens[p.pos]
	v := token.value
	p.pos++
	if token.literal {
		return &queryNode{op: "literal", value: v}, nil
	}
	if strings.HasPrefix(v, "-") && len(v) > 1 {
		return &queryNode{op: "NOT", left: &queryNode{op: "term", value: v[1:]}}, nil
	}
	return &queryNode{op: "term", value: v}, nil
}
func compileQuery(n *queryNode, alias, userID string) (string, []any, error) {
	if n == nil {
		return "1", nil, nil
	}
	if n.op == "NOT" {
		q, a, e := compileQuery(n.left, alias, userID)
		return "NOT (" + q + ")", a, e
	}
	if n.op == "AND" || n.op == "OR" {
		l, a, e := compileQuery(n.left, alias, userID)
		if e != nil {
			return "", nil, e
		}
		r, b, e := compileQuery(n.right, alias, userID)
		return "(" + l + ") " + n.op + " (" + r + ")", append(a, b...), e
	}
	value := n.value
	field := ""
	if prefix, rest, ok := strings.Cut(value, ":"); ok && n.op != "literal" {
		switch prefix {
		case "root", "path", "title", "filename", "tag", "word", "substring", "exact", "prefix", "favorite":
			field = prefix
			value = rest
		default:
			field = "tag"
		}
	}
	if value == "" {
		return "", nil, errors.New("empty field predicate")
	}
	if field == "root" {
		return alias + ".root_id=?", []any{value}, nil
	}
	if field == "favorite" {
		if value != "true" && value != "false" {
			return "", nil, errors.New("favorite requires true or false")
		}
		expr := "EXISTS(SELECT 1 FROM favorites f WHERE f.work_id=" + alias + ".work_id AND f.user_id=?)"
		if value == "false" {
			expr = "NOT " + expr
		}
		return expr, []any{userID}, nil
	}
	if field == "tag" {
		tag := normalizeTag(value)
		if n.value != "tag:"+value && strings.Contains(n.value, ":") && !strings.HasPrefix(n.value, "tag:") {
			tag = normalizeTag(n.value)
		}
		return "((" + alias + ".id IN (SELECT alias_id FROM alias_tags WHERE tag=?) AND NOT EXISTS(SELECT 1 FROM tag_decisions d WHERE d.work_id=" + alias + ".work_id AND d.tag=? AND d.decision=-1)) OR " + alias + ".work_id IN (SELECT work_id FROM effective_tags WHERE tag=?))", []any{tag, tag, tag}, nil
	}
	value = normalizeSearch(value)
	table := "alias_substrings"
	metaTable := "metadata_substrings"
	queryValue := "\"" + strings.ReplaceAll(value, "\"", "\"\"") + "\""
	short := utf8.RuneCountInString(value) < 3
	if field == "word" || field == "prefix" || field == "exact" || short {
		table = "alias_words"
		metaTable = "metadata_words"
		if field != "word" && field != "exact" {
			queryValue += "*"
		}
	}
	column := ""
	switch field {
	case "path":
		column = "path"
	case "title", "filename":
		column = "title"
	}
	if column != "" {
		queryValue = column + " : " + queryValue
	}
	aliasExpr := fmt.Sprintf("%s.id IN (SELECT rowid FROM %s WHERE %s MATCH ?)", alias, table, table)
	if column != "" {
		return aliasExpr, []any{queryValue}, nil
	}
	return "(" + aliasExpr + " OR " + alias + ".work_id IN (SELECT work_id FROM " + metaTable + " WHERE " + metaTable + " MATCH ?))", []any{queryValue, queryValue}, nil
}

const searchSchema = `
CREATE TABLE IF NOT EXISTS alias_tags(alias_id INTEGER NOT NULL REFERENCES aliases(id) ON DELETE CASCADE,tag TEXT NOT NULL,PRIMARY KEY(alias_id,tag));
CREATE INDEX IF NOT EXISTS alias_tags_tag ON alias_tags(tag,alias_id);
CREATE TRIGGER IF NOT EXISTS alias_tags_insert AFTER INSERT ON aliases BEGIN INSERT OR IGNORE INTO alias_tags(alias_id,tag) SELECT new.id,lower(value) FROM json_each(new.filename_tags);END;
 CREATE TRIGGER IF NOT EXISTS alias_tags_update AFTER UPDATE OF filename_tags ON aliases WHEN old.filename_tags IS NOT new.filename_tags BEGIN DELETE FROM alias_tags WHERE alias_id=old.id;INSERT OR IGNORE INTO alias_tags(alias_id,tag) SELECT new.id,lower(value) FROM json_each(new.filename_tags);END;
CREATE TABLE IF NOT EXISTS effective_tags(work_id TEXT NOT NULL REFERENCES works(id) ON DELETE CASCADE,tag TEXT NOT NULL,PRIMARY KEY(work_id,tag));
CREATE INDEX IF NOT EXISTS effective_tags_tag ON effective_tags(tag,work_id);
CREATE VIRTUAL TABLE IF NOT EXISTS metadata_words USING fts5(work_id UNINDEXED,text,tokenize='unicode61',prefix='1 2 3');
CREATE VIRTUAL TABLE IF NOT EXISTS metadata_substrings USING fts5(work_id UNINDEXED,text,tokenize='trigram');
 CREATE TRIGGER IF NOT EXISTS work_search_delete AFTER DELETE ON works BEGIN DELETE FROM metadata_words WHERE work_id=old.id;DELETE FROM metadata_substrings WHERE work_id=old.id;END;
`
