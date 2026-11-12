package admin

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/build"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/edit"
	"github.com/WhiteRoomLab/whiteroom/internal/publish"
)

// operations returns n requests of operations that saved the terms with the numbered IDs.
func operations(at time.Time, n int) []publish.Request {
	out := make([]publish.Request, n)
	for i := range out {
		out[i] = publish.Request{
			At: at, Reason: "operation",
			Result: edit.Result{Kind: content.RefTerm, ID: fmt.Sprintf("trm_01JD00000000000000000%05d", i)},
		}
	}
	return out
}

func problems100(n int, file string) content.Errors {
	es := make(content.Errors, n)
	for i := range es {
		es[i] = &content.Error{File: file, Path: fmt.Sprintf("/items/%d", i), Msg: fmt.Sprintf("problem %d", i), Line: 1 + i, Column: 3}
	}
	return es
}

// TestBuildsOf shows the JSON that the state of the builds has: what is not there is not written, the problems of the site are
// listed (the first 100), and a failure that is none of them is a fixed sentence and no more.
func TestBuildsOf(t *testing.T) {
	jst := time.FixedZone("JST", 9*3600)
	start := time.Date(2026, 11, 9, 21, 0, 0, 0, jst) // 12:00:00Z
	end := start.Add(2 * time.Second)
	const (
		started = `"startedAt":"2026-11-09T12:00:00Z"`
		ended   = `"endedAt":"2026-11-09T12:00:02Z"`
		at      = `"at":"2026-11-09T12:00:00Z"`
	)
	entry := "ent_01JD0000000000000000000001"
	secret := "/srv/secret/out/releases/.tmp-0123: permission denied"

	for _, tt := range []struct {
		name string
		st   publish.Status
		want string
	}{
		{"nothing", publish.Status{}, `{"pending":false,"paused":false,"schedules":[]}`},
		{"waiting while paused", publish.Status{Pending: true, Paused: true}, `{"pending":true,"paused":true,"schedules":[]}`},
		{
			"a build that runs",
			publish.Status{Running: &publish.Build{Started: start, Requests: []publish.Request{
				{At: start, Reason: "start"},
				{At: start, Reason: "operation", Result: edit.Result{Kind: content.RefEntry, ID: entry, Status: content.StatusPublished, Publishes: true}},
				{At: start, Reason: "operation", Result: edit.Result{Kind: "settings"}},
				{At: start, Reason: "rebuild"},
				{At: start, Reason: "resume", Result: edit.Result{Kind: content.RefEntry, ID: entry, Status: content.StatusDraft}},
			}}},
			`{"running":{` + started + `,"requests":[{` + at + `,"reason":"start"},` +
				`{` + at + `,"reason":"operation","kind":"entry","id":"` + entry + `","status":"published"},` +
				`{` + at + `,"reason":"operation","kind":"settings"},{` + at + `,"reason":"rebuild"},{` + at + `,"reason":"resume"}]},` +
				`"pending":false,"paused":false,"schedules":[]}`,
		},
		{
			"a build that has published",
			publish.Status{Last: &publish.Build{Started: start, Ended: end, Requests: []publish.Request{{At: start, Reason: "start"}}, Result: &build.Result{
				Release: "0123456789abcdef", Changed: false, Pages: 0, Files: 0,
				Warnings: []*content.Error{{File: "menus/mnu_1.json", Path: "/items/1", Msg: "left out", Line: 3, Column: 5}, {Path: "", Msg: "theme"}},
			}}},
			`{"pending":false,"paused":false,"last":{` + started + `,` + ended + `,"requests":[{` + at + `,"reason":"start"}],` +
				`"release":"0123456789abcdef","changed":false,"pages":0,"files":0,` +
				`"warnings":[{"file":"menus/mnu_1.json","pointer":"/items/1","line":3,"column":5,"detail":"left out"},{"pointer":"","detail":"theme"}],` +
				`"failed":false},"schedules":[]}`,
		},
		{
			"a build that failed for the site",
			publish.Status{Last: &publish.Build{Started: start, Ended: end, Requests: []publish.Request{{At: start, Reason: "rebuild"}},
				Err: content.Errors{{File: "terms/trm_1.json", Path: "/taxonomy", Msg: `unknown taxonomy "x"`, Line: 1, Column: 58}}}},
			`{"pending":false,"paused":false,"last":{` + started + `,` + ended + `,"requests":[{` + at + `,"reason":"rebuild"}],` +
				`"failed":true,"errors":[{"file":"terms/trm_1.json","pointer":"/taxonomy","line":1,"column":58,"detail":"unknown taxonomy \"x\""}]},"schedules":[]}`,
		},
		{
			"a build that failed for the site, in an error that wraps it",
			publish.Status{Last: &publish.Build{Started: start, Ended: end, Err: fmt.Errorf("reading the store: %w", content.Errors{{Path: "/a", Msg: "b"}})}},
			`{"pending":false,"paused":false,"last":{` + started + `,` + ended + `,"requests":[],` +
				`"failed":true,"errors":[{"pointer":"/a","detail":"b"}]},"schedules":[]}`,
		},
		{
			"a build that failed for another reason",
			publish.Status{Last: &publish.Build{Started: start, Ended: end, Err: &fs.PathError{Op: "mkdir", Path: secret, Err: fs.ErrPermission}}},
			`{"pending":false,"paused":false,"last":{` + started + `,` + ended + `,"requests":[],` +
				`"failed":true,"error":"the build failed for a reason other than the content of the site"},"schedules":[]}`,
		},
		{
			"a build that published and failed for another reason",
			publish.Status{Last: &publish.Build{Started: start, Ended: end, Result: &build.Result{Release: "0123456789abcdef", Changed: true, Pages: 7, Files: 9},
				Err: errors.New("pruning the cache: " + secret)}},
			`{"pending":false,"paused":false,"last":{` + started + `,` + ended + `,"requests":[],` +
				`"release":"0123456789abcdef","changed":true,"pages":7,"files":9,` +
				`"failed":true,"error":"the build failed for a reason other than the content of the site"},"schedules":[]}`,
		},
		{
			"an error that holds no problem",
			publish.Status{Last: &publish.Build{Started: start, Ended: end, Err: content.Errors{}}},
			`{"pending":false,"paused":false,"last":{` + started + `,` + ended + `,"requests":[],` +
				`"failed":true,"error":"the build failed for a reason other than the content of the site"},"schedules":[]}`,
		},
		{
			"scheduled publishes that failed",
			publish.Status{Schedules: []publish.ScheduleFailure{
				{Entry: entry, At: start, Tried: end, Err: content.Errors{{File: "entries/" + entry + ".json", Path: "/slug", Msg: "taken"}}},
				{Entry: "ent_01JD0000000000000000000002", Tried: end, Err: errors.New(secret)},
				{Tried: end, Err: errors.New(secret)},
			}},
			`{"pending":false,"paused":false,"schedules":[` +
				`{"entry":"` + entry + `","at":"2026-11-09T12:00:00Z","tried":"2026-11-09T12:00:02Z","errors":[{"file":"entries/` + entry + `.json","pointer":"/slug","detail":"taken"}]},` +
				`{"entry":"ent_01JD0000000000000000000002","tried":"2026-11-09T12:00:02Z","error":"the scheduled publish failed for a reason other than the content of the site"},` +
				`{"tried":"2026-11-09T12:00:02Z","error":"the scheduled publish failed for a reason other than the content of the site"}]}`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			b, err := json.Marshal(buildsOf(tt.st))
			if err != nil {
				t.Fatal(err)
			}
			if string(b) != tt.want {
				t.Errorf("the state is\n%s\nwant\n%s", b, tt.want)
			}
			if strings.Contains(string(b), "/srv/secret") {
				t.Errorf("the state names a path of the server: %s", b)
			}
		})
	}
}

// TestBuildsOfLimits shows that the lists of an answer have at most 100 items, and the others are counted.
func TestBuildsOfLimits(t *testing.T) {
	start := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	st := publish.Status{
		Running: &publish.Build{Started: start, Requests: operations(start, 150)},
		Last: &publish.Build{Started: start, Ended: start, Requests: operations(start, 100),
			Result: &build.Result{Release: "0123456789abcdef", Warnings: problems100(101, "menus/x.json")}},
		Schedules: []publish.ScheduleFailure{{Entry: "ent_01JD0000000000000000000001", Tried: start, Err: problems100(250, "entries/x.json")}},
	}
	got := buildsOf(st)
	if r := got.Running; len(r.Requests) != 100 || r.OmittedRequests != 50 || r.Requests[0].ID != "trm_01JD0000000000000000000000" ||
		r.Requests[99].ID != "trm_01JD0000000000000000000099" {
		t.Errorf("the running build lists %d requests and omits %d, want the first 100 and 50", len(r.Requests), r.OmittedRequests)
	}
	if l := got.Last; len(l.Requests) != 100 || l.OmittedRequests != 0 || len(l.Warnings) != 100 || l.OmittedWarnings != 1 || l.Warnings[0].Detail != "problem 0" {
		t.Errorf("the last build lists %d requests, omits %d, and lists %d warnings, omits %d, want 100, 0, 100 and 1",
			len(l.Requests), l.OmittedRequests, len(l.Warnings), l.OmittedWarnings)
	}
	if s := got.Schedules[0]; len(s.Errors) != 100 || s.OmittedErrors != 150 || s.Errors[99].Detail != "problem 99" {
		t.Errorf("the failed schedule lists %d errors and omits %d, want the first 100 and 150", len(s.Errors), s.OmittedErrors)
	}

	st = publish.Status{Last: &publish.Build{Started: start, Ended: start, Err: problems100(120, "terms/x.json")}}
	if l := buildsOf(st).Last; l.Failed == nil || !*l.Failed || len(l.Errors) != 100 || l.OmittedErrors != 20 || l.Error != "" {
		t.Errorf("a build with 120 problems of the site lists %d errors and omits %d (error %q), want 100 and 20", len(l.Errors), l.OmittedErrors, l.Error)
	}
}
