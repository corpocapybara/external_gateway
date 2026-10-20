//go:build windows

package main

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// yamlFile is a YAML document edited as a node tree but written back as a minimal line patch on
// the original text, so blank lines, comment spacing and untouched sections stay byte-identical.
type yamlFile struct {
	path     string
	original string
	doc      yaml.Node
}

func openYAML(path string) (*yamlFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	f := &yamlFile{path: path, original: string(data)}
	if err := yaml.Unmarshal(data, &f.doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(f.doc.Content) == 0 {
		f.doc.Kind = yaml.DocumentNode
		f.doc.Content = []*yaml.Node{{Kind: yaml.MappingNode}}
	}
	return f, nil
}

func (f *yamlFile) root() *yaml.Node { return f.doc.Content[0] }

func renderNode(n *yaml.Node) (string, error) {
	var b bytes.Buffer
	e := yaml.NewEncoder(&b)
	e.SetIndent(2)
	if err := e.Encode(n); err != nil {
		return "", err
	}
	e.Close()
	return b.String(), nil
}

// patched returns the new file text: the structural change between the original and the edited
// tree, applied onto the original lines.
func (f *yamlFile) patched() (string, error) {
	var origDoc yaml.Node
	if err := yaml.Unmarshal([]byte(f.original), &origDoc); err != nil {
		return "", err
	}
	r0, err := renderNode(&origDoc)
	if err != nil {
		return "", err
	}
	r1, err := renderNode(&f.doc)
	if err != nil {
		return "", err
	}
	if r0 == r1 {
		return f.original, nil
	}
	newLineEnd := ""
	if 2*strings.Count(f.original, "\r\n") > strings.Count(f.original, "\n") {
		newLineEnd = "\r"
	}
	var orig []string
	if body := strings.TrimSuffix(f.original, "\n"); body != "" {
		orig = strings.Split(body, "\n")
	}
	a := splitLines(r0)
	b := splitLines(r1)

	norm := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	var origKeys, aKeys []string
	var origIdx []int
	for i, l := range orig {
		if strings.TrimSpace(l) != "" {
			origKeys = append(origKeys, norm(l))
			origIdx = append(origIdx, i)
		}
	}
	for _, l := range a {
		aKeys = append(aKeys, norm(l))
	}
	aToOrig := map[int]int{}
	for _, op := range diffLines(aKeys, origKeys) {
		if op.kind == '=' {
			aToOrig[op.i] = origIdx[op.j]
		}
	}
	mapped := map[int]bool{}
	for _, o := range aToOrig {
		mapped[o] = true
	}

	var out []string
	last := -1
	flushTo := func(o int) {
		for k := last + 1; k < o; k++ {
			if !mapped[k] {
				out = append(out, orig[k])
			}
		}
	}
	for _, op := range diffLines(a, b) {
		switch op.kind {
		case '=':
			if o, ok := aToOrig[op.i]; ok && o > last {
				flushTo(o)
				out = append(out, orig[o])
				last = o
			}
		case '-':
			if o, ok := aToOrig[op.i]; ok && o > last {
				flushTo(o)
				last = o
			}
		case '+':
			out = append(out, b[op.j]+newLineEnd)
		}
	}
	flushTo(len(orig))
	text := strings.Join(out, "\n")
	if strings.HasSuffix(f.original, "\n") {
		text += "\n"
	}
	return text, nil
}

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

type diffOp struct {
	kind byte
	i, j int
}

// diffLines is an LCS line diff: '=' keeps a[i]==b[j], '-' drops a[i], '+' adds b[j].
func diffLines(a, b []string) []diffOp {
	n, m := len(a), len(b)
	lcs := make([][]int32, n+1)
	for i := range lcs {
		lcs[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	var ops []diffOp
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffOp{'=', i, j})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			ops = append(ops, diffOp{'-', i, j})
			i++
		default:
			ops = append(ops, diffOp{'+', i, j})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOp{'-', i, j})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOp{'+', i, j})
	}
	return ops
}

func scalar(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
}

func typedScalar(v, tag string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: v}
}

func mapping(kv ...interface{}) *yaml.Node {
	n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for i := 0; i+1 < len(kv); i += 2 {
		k := kv[i].(string)
		switch v := kv[i+1].(type) {
		case *yaml.Node:
			if v != nil {
				n.Content = append(n.Content, scalar(k), v)
			}
		case string:
			if v != "" {
				n.Content = append(n.Content, scalar(k), scalar(v))
			}
		}
	}
	return n
}

func mapGet(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func mapSet(m *yaml.Node, key string, v *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = v
			return
		}
	}
	m.Content = append(m.Content, scalar(key), v)
}

func mapDelete(m *yaml.Node, key string) bool {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return true
		}
	}
	return false
}

func mapEnsure(m *yaml.Node, key string) *yaml.Node {
	if v := mapGet(m, key); v != nil {
		if v.Kind != yaml.MappingNode {
			v.Kind, v.Tag, v.Value, v.Content = yaml.MappingNode, "!!map", "", nil
		}
		return v
	}
	v := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	mapSet(m, key, v)
	return v
}

func seqEnsure(m *yaml.Node, key string) *yaml.Node {
	if v := mapGet(m, key); v != nil && v.Kind == yaml.SequenceNode {
		return v
	}
	v := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	mapSet(m, key, v)
	return v
}

func seqHas(s *yaml.Node, v string) bool {
	if s == nil {
		return false
	}
	for _, c := range s.Content {
		if c.Value == v {
			return true
		}
	}
	return false
}

func seqRemove(s *yaml.Node, v string) bool {
	if s == nil {
		return false
	}
	for i, c := range s.Content {
		if c.Value == v {
			s.Content = append(s.Content[:i], s.Content[i+1:]...)
			return true
		}
	}
	return false
}

func mapKeys(m *yaml.Node) []string {
	keys := []string{}
	if m == nil || m.Kind != yaml.MappingNode {
		return keys
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		keys = append(keys, m.Content[i].Value)
	}
	return keys
}

var identRe = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
