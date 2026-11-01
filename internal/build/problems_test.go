package build

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/WhiteRoomLab/whiteroom/internal/content"
)

func TestProblems(t *testing.T) {
	entry := content.SiteFile{Name: "entries/" + aboutID + ".json", Data: []byte("{\n  \"schemaVersion\": 1,\n  \"id\": \"x\"\n}\n")}
	term := content.SiteFile{Name: "terms/" + eventTagID + ".json", Data: []byte("{\n  \"name\": \"イベント\"\n}\n")}
	var p problems
	if got := p.sorted(); got != nil || p.len() != 0 {
		t.Errorf("an empty problems has %d: %v", p.len(), got)
	}
	p.add(term, "/name", "b")
	p.add(term, "/name", "a")
	p.add(term, "/slug", "missing slug")
	p.addErr(entry, content.Errors{{Path: "/id", Msg: "m"}, {Path: "/number", Msg: "missing number"}})
	p.addErr(entry, errors.New("boom"))
	p.addErr(entry, nil)
	p.add(entry, "/id", "m")
	p.addLocated(content.Errors{{File: term.Name, Line: 2, Column: 3, Path: "/name", Msg: "a"}, {File: term.Name, Line: 1, Msg: "z"}})
	p.add(content.SiteFile{Name: "theme"}, "", "fallback")
	p.add(content.SiteFile{}, "", "core")
	if n := p.len(); n != 11 {
		t.Errorf("len() = %d, want 11", n)
	}
	want := []string{
		"core",
		"entries/" + aboutID + ".json:1:1: boom",
		"entries/" + aboutID + ".json:1:1: /number: missing number",
		"entries/" + aboutID + ".json:3:3: /id: m",
		"terms/" + eventTagID + ".json:1: z",
		"terms/" + eventTagID + ".json:1:1: /slug: missing slug",
		"terms/" + eventTagID + ".json:2:3: /name: a",
		"terms/" + eventTagID + ".json:2:3: /name: b",
		"theme: fallback",
	}
	if got := errorTexts(t, p.sorted()); !slices.Equal(got, want) {
		t.Errorf("sorted():\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestProblemsConcurrent(t *testing.T) {
	f := content.SiteFile{Name: "entries/" + aboutID + ".json", Data: []byte(`{"schemaVersion": 1}`)}
	var p problems
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for j := range 10 {
				p.add(f, fmt.Sprintf("/a/%d", j), "m")
				p.addLocated(content.Errors{{File: "theme", Msg: fmt.Sprint(i)}})
			}
		})
	}
	wg.Wait()
	if n := p.len(); n != 8*10*2 {
		t.Errorf("len() = %d, want %d", n, 8*10*2)
	}
	if n := len(p.sorted()); n != 10+8 {
		t.Errorf("sorted() has %d problems, want %d", n, 10+8)
	}
}
