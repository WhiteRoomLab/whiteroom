package main

import (
	"fmt"
	"os"
	"os/user"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/configfile"
	"github.com/WhiteRoomLab/whiteroom/internal/dbwire"
)

// currentNames returns the name of the user and of the group of this process, or "" for one that the system does not
// know by a name.
func currentNames(t *testing.T) (userName, groupName string) {
	t.Helper()
	if u, err := user.LookupId(strconv.Itoa(os.Getuid())); err == nil {
		userName = u.Username
	}
	if g, err := user.LookupGroupId(strconv.Itoa(os.Getgid())); err == nil {
		groupName = g.Name
	}
	return userName, groupName
}

// resolved runs the flags of whiteroom-db serve, with -data d and -db s, and with the config file that has yaml unless it
// is "", and returns the settings that they make, whether they are good, and what the command prints on stderr.
func resolved(t *testing.T, yaml string, args ...string) (s serveSettings, ok bool, stderr string) {
	t.Helper()
	var e syncBuffer
	flags, f := newServeFlags(&e)
	args = append([]string{"-data", "d", "-db", "s"}, args...)
	if yaml != "" {
		args = append([]string{"-config", writeFile(t, t.TempDir(), "whiteroom.yaml", yaml)}, args...)
	}
	if err := flags.Parse(args); err != nil {
		t.Fatalf("Parse(%q) = %v", args, err)
	}
	cfg, err := configfile.Load(f.config)
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	s, ok = f.resolve(flags, cfg, &e)
	return s, ok, e.String()
}

func TestResolve(t *testing.T) {
	uid := uint32(os.Getuid())
	userName, groupName := currentNames(t)
	gid := os.Getgid()
	defaults := dbwire.Limits{Readers: 64, Staged: 268435446, CommitsPerSecond: 50, MinFree: 1 << 30, MinFreeInodes: 65536,
		SnapshotIdle: 5 * time.Minute, Stall: 30 * time.Second}
	if got := defaults.WithDefaults(); got.Readers != 64 || got.Staged != 268435446 || got.CommitsPerSecond != 50 ||
		got.MinFree != 1<<30 || got.MinFreeInodes != 65536 || got.SnapshotIdle != 5*time.Minute || got.Stall != 30*time.Second {
		t.Fatalf("the defaults of the daemon are not those that the usage tells: %+v", got)
	}
	base := serveSettings{data: "d", db: "s", writerUID: uid, writerGID: -1, readerGID: -1, limits: defaults}
	with := func(f func(*serveSettings)) serveSettings {
		s := base
		f(&s)
		return s
	}

	for _, tt := range []struct {
		name string
		yaml string
		args []string
		want serveSettings
	}{
		{"defaults", "", nil, base},
		{"flags", "", []string{"-writer-user", "7", "-writer-group", "8", "-reader-group", "9", "-max-readers", "5", "-commit-rate", "6",
			"-min-free", "7", "-min-free-inodes", "11", "-snapshot-idle", "8s", "-stall", "9m", "-max-staged", "10"},
			with(func(s *serveSettings) {
				s.writerUID, s.writerGID, s.readerGID = 7, 8, 9
				s.limits = dbwire.Limits{Readers: 5, CommitsPerSecond: 6, MinFree: 7, MinFreeInodes: 11, SnapshotIdle: 8 * time.Second,
					Stall: 9 * time.Minute, Staged: 10}
			})},
		{"items", "writerUser: 7\nwriterGroup: 8\nreaderGroup: \"9\"\nmaxReaders: 5\ncommitRate: 6\nminFree: 7\nminFreeInodes: 11\n" +
			"snapshotIdle: 8s\nstall: 9m\nmaxStaged: 10\n", nil,
			with(func(s *serveSettings) {
				s.writerUID, s.writerGID, s.readerGID = 7, 8, 9
				s.limits = dbwire.Limits{Readers: 5, CommitsPerSecond: 6, MinFree: 7, MinFreeInodes: 11, SnapshotIdle: 8 * time.Second,
					Stall: 9 * time.Minute, Staged: 10}
			})},
		{"flags over items", "writerUser: 7\nwriterGroup: 8\nreaderGroup: 9\nmaxReaders: 5\ncommitRate: 6\nminFree: 7\nminFreeInodes: 11\n" +
			"snapshotIdle: 8s\nstall: 9m\nmaxStaged: 10\n",
			[]string{"-writer-user", "1", "-writer-group", "2", "-reader-group", "3", "-max-readers", "4", "-commit-rate", "5",
				"-min-free", "6", "-min-free-inodes", "10", "-snapshot-idle", "7s", "-stall", "8m", "-max-staged", "9"},
			with(func(s *serveSettings) {
				s.writerUID, s.writerGID, s.readerGID = 1, 2, 3
				s.limits = dbwire.Limits{Readers: 4, CommitsPerSecond: 5, MinFree: 6, MinFreeInodes: 10, SnapshotIdle: 7 * time.Second,
					Stall: 8 * time.Minute, Staged: 9}
			})},
		// 0 turns a limit off, which the daemon says with a negative value, as its 0 is the default; a flag with the value
		// 0 overrides the item, and an item with the value 0 overrides the default.
		{"flags of 0", "commitRate: 6\nminFree: 7\nminFreeInodes: 8\nsnapshotIdle: 8s\nstall: 9m\n",
			[]string{"-commit-rate", "0", "-min-free", "0", "-min-free-inodes", "0", "-snapshot-idle", "0", "-stall", "0s"},
			with(func(s *serveSettings) {
				s.limits.CommitsPerSecond, s.limits.MinFree, s.limits.SnapshotIdle, s.limits.Stall = -1, -1, -1, -1
				s.limits.MinFreeInodes = -1
			})},
		{"items of 0", "commitRate: 0\nminFree: 0\nminFreeInodes: 0\nsnapshotIdle: 0s\nstall: \"0\"\n", nil,
			with(func(s *serveSettings) {
				s.limits.CommitsPerSecond, s.limits.MinFree, s.limits.SnapshotIdle, s.limits.Stall = -1, -1, -1, -1
				s.limits.MinFreeInodes = -1
			})},
		{"inodes only", "minFreeInodes: 0\n", []string{"-min-free", "0", "-min-free-inodes", "12"},
			with(func(s *serveSettings) { s.limits.MinFree, s.limits.MinFreeInodes = -1, 12 })},
		{"flags that are the defaults", "commitRate: 6\n", []string{"-commit-rate", "50"}, base},
		{"the file does not name the limits", "workers: 2\nurl: https://example.com\n", nil, base},
		{"writer-user 0", "", []string{"-writer-user", "0"}, with(func(s *serveSettings) { s.writerUID = 0 })},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, ok, stderr := resolved(t, tt.yaml, tt.args...)
			if !ok || stderr != "" {
				t.Fatalf("resolve = %v, with stderr %q, want true and nothing", ok, stderr)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("resolve =\n%+v\nwant\n%+v", got, tt.want)
			}
		})
	}

	t.Run("names", func(t *testing.T) {
		if userName == "" || groupName == "" {
			t.Skip("the system has no name for the user or the group of this process")
		}
		for _, form := range []struct {
			name, yaml string
			args       []string
		}{
			{"flags", "", []string{"-writer-user", userName, "-writer-group", groupName, "-reader-group", groupName}},
			{"items", fmt.Sprintf("writerUser: %q\nwriterGroup: %q\nreaderGroup: %q\n", userName, groupName, groupName), nil},
			{"numbers", fmt.Sprintf("writerUser: %d\nwriterGroup: %d\nreaderGroup: \"%d\"\n", uid, gid, gid), nil},
		} {
			got, ok, stderr := resolved(t, form.yaml, form.args...)
			want := with(func(s *serveSettings) { s.writerGID, s.readerGID = gid, gid })
			if !ok || stderr != "" || !reflect.DeepEqual(got, want) {
				t.Errorf("%s: resolve = %+v, %v, with stderr %q, want %+v", form.name, got, ok, stderr, want)
			}
		}
	})

	// A value that is wrong is a usage error for a flag, and a problem of the item for the config file, before anything is
	// opened.
	for _, tt := range []struct {
		name string
		yaml string
		args []string
		want string // the first line
	}{
		{"unknown user", "", []string{"-writer-user", "no-such-user-x"}, `whiteroom-db serve: -writer-user: unknown user "no-such-user-x"`},
		{"user -1", "", []string{"-writer-user", "-1"}, `whiteroom-db serve: -writer-user: unknown user "-1"`},
		{"empty user", "", []string{"-writer-user", ""}, "whiteroom-db serve: -writer-user: must be a name or a number"},
		{"big user", "", []string{"-writer-user", "4294967296"}, "whiteroom-db serve: -writer-user: the number is over 4294967295"},
		{"unknown writer group", "", []string{"-writer-group", "no-such-group-x"},
			`whiteroom-db serve: -writer-group: unknown group "no-such-group-x"`},
		{"unknown reader group", "", []string{"-reader-group", "no-such-group-x"},
			`whiteroom-db serve: -reader-group: unknown group "no-such-group-x"`},
		{"empty group", "", []string{"-reader-group", ""}, "whiteroom-db serve: -reader-group: must be a name or a number"},
		{"item: unknown user", "writerUser: no-such-user-x\n", nil, `/writerUser: unknown user "no-such-user-x"`},
		{"item: user -1", "writerUser: \"-1\"\n", nil, `/writerUser: unknown user "-1"`},
		{"item: unknown writer group", "writerGroup: no-such-group-x\n", nil, `/writerGroup: unknown group "no-such-group-x"`},
		{"item: unknown reader group", "readerGroup: no-such-group-x\n", nil, `/readerGroup: unknown group "no-such-group-x"`},
		{"max-readers 0", "", []string{"-max-readers", "0"}, "whiteroom-db serve: -max-readers: must be an integer of 1 or more"},
		{"max-readers -1", "", []string{"-max-readers", "-1"}, "whiteroom-db serve: -max-readers: must be an integer of 1 or more"},
		{"commit-rate -1", "", []string{"-commit-rate", "-1"}, "whiteroom-db serve: -commit-rate: must be an integer of 0 or more"},
		{"min-free -1", "", []string{"-min-free", "-1"}, "whiteroom-db serve: -min-free: must be an integer of 0 or more"},
		{"min-free-inodes -1", "", []string{"-min-free-inodes", "-1"},
			"whiteroom-db serve: -min-free-inodes: must be an integer of 0 or more"},
		{"snapshot-idle -1s", "", []string{"-snapshot-idle", "-1s"},
			"whiteroom-db serve: -snapshot-idle: must be a duration of 0 or more, such as 30s or 5m"},
		{"stall -1s", "", []string{"-stall", "-1s"}, "whiteroom-db serve: -stall: must be a duration of 0 or more, such as 30s or 5m"},
		{"max-staged 0", "", []string{"-max-staged", "0"}, "whiteroom-db serve: -max-staged: must be an integer of 1 or more"},
		{"max-staged -1", "", []string{"-max-staged", "-1"}, "whiteroom-db serve: -max-staged: must be an integer of 1 or more"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, ok, stderr := resolved(t, tt.yaml, tt.args...)
			first, _, _ := strings.Cut(stderr, "\n")
			if tt.yaml != "" {
				// The line of a problem in the file starts with its name, which the temporary directory makes.
				if _, rest, found := strings.Cut(first, ".yaml: "); found {
					first = rest
				}
				if !strings.HasSuffix(stderr, "\n") || strings.Count(stderr, "\n") != 1 {
					t.Errorf("the problem of the file is not one line:\n%s", stderr)
				}
			} else if !strings.Contains(stderr, serveUsage) {
				t.Errorf("a usage error has no usage text:\n%s", stderr)
			}
			if ok || first != tt.want {
				t.Errorf("resolve = %v, with the first line %q of stderr, want false and %q", ok, first, tt.want)
			}
		})
	}
}
