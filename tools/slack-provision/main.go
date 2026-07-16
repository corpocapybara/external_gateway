// slack-extract reads a Chromium/Electron "Local Storage" LevelDB (as used by the
// Slack desktop app) and emits the per-workspace session tokens it finds.
//
// It exists because the tokens are stored snappy-compressed inside LevelDB SSTables,
// so a naive byte scan only recovers fragments. goleveldb decompresses them.
//
// Usage:  slack-extract <leveldb-dir> <out.json>
// Output: JSON array of {domain,name,url,token} (one per signed-in workspace).
//
// This is an operator tool, kept in its own module so goleveldb never enters the
// gateway's dependency graph. It reads nothing but the given directory.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"unicode/utf16"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
)

type team struct {
	Domain string `json:"domain"`
	Name   string `json:"name"`
	URL    string `json:"url"`
	Token  string `json:"token"`
}

// Chromium localStorage values carry a 1-byte encoding prefix (0 = UTF-16LE, 1 = Latin-1).
// Return the raw string plus both candidate decodings so the regexes below can match.
func decodings(v []byte) []string {
	out := []string{string(v)}
	if len(v) > 1 {
		body := v[1:]
		out = append(out, string(body))
		if len(body)%2 == 0 {
			u := make([]uint16, len(body)/2)
			for i := range u {
				u[i] = uint16(body[2*i]) | uint16(body[2*i+1])<<8
			}
			out = append(out, string(utf16.Decode(u)))
		}
	}
	return out
}

var (
	tokRe = regexp.MustCompile(`"token":"(xoxc-[0-9A-Za-z-]{20,})"`)
	domRe = regexp.MustCompile(`"domain":"([^"]{1,64})"`)
	namRe = regexp.MustCompile(`"name":"([^"]{1,80})"`)
	urlRe = regexp.MustCompile(`"url":"(https://[^"]+)"`)
)

func lastSubmatch(re *regexp.Regexp, s string) string {
	m := re.FindAllStringSubmatch(s, -1)
	if len(m) == 0 {
		return ""
	}
	return m[len(m)-1][1]
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: slack-extract <leveldb-dir> <out.json>")
		os.Exit(2)
	}
	db, err := leveldb.OpenFile(os.Args[1], &opt.Options{ReadOnly: true})
	if err != nil {
		fmt.Fprintln(os.Stderr, "open leveldb:", err)
		os.Exit(1)
	}
	defer db.Close()

	teams := map[string]*team{}
	iter := db.NewIterator(nil, nil)
	for iter.Next() {
		for _, s := range decodings(iter.Value()) {
			if !strings.Contains(s, `"token":"xoxc-`) {
				continue
			}
			for _, m := range tokRe.FindAllStringSubmatchIndex(s, -1) {
				tok := s[m[2]:m[3]]
				if _, ok := teams[tok]; ok {
					continue
				}
				lo := m[0] - 900
				if lo < 0 {
					lo = 0
				}
				hi := m[1] + 400
				if hi > len(s) {
					hi = len(s)
				}
				pre := s[lo:m[0]]  // team fields usually precede "token"
				post := s[m[1]:hi] // "url" usually follows it
				t := &team{Token: tok}
				t.Domain = lastSubmatch(domRe, pre)
				t.Name = lastSubmatch(namRe, pre)
				if x := urlRe.FindStringSubmatch(post); x != nil {
					t.URL = x[1]
				} else {
					t.URL = lastSubmatch(urlRe, pre)
				}
				teams[tok] = t
			}
		}
	}
	iter.Release()
	if err := iter.Error(); err != nil {
		fmt.Fprintln(os.Stderr, "iterate:", err)
	}

	list := make([]*team, 0, len(teams))
	for _, t := range teams {
		list = append(list, t)
	}
	b, _ := json.MarshalIndent(list, "", "  ")
	if err := os.WriteFile(os.Args[2], b, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "write out:", err)
		os.Exit(1)
	}
	fmt.Printf("extracted %d team token(s)\n", len(list))
}
