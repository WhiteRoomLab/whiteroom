package publish

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/WhiteRoomLab/whiteroom/internal/audit"
	"github.com/WhiteRoomLab/whiteroom/internal/build"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

const (
	// recordTimeout is how long the record of what has ended, which the end of a context does not stop, waits for the store.
	recordTimeout = 5 * time.Second
	// resultFailed is the result of the record of a build or a rollback that has failed.
	resultFailed = "failed"
	// maxReleaseName is the most bytes of the name of a release that a record keeps; the names that a build makes have 16.
	maxReleaseName = 64
)

// requestKeys are the keys of the detail of a build record that count the requests of each reason.
var requestKeys = []struct{ reason, key string }{
	{reasonStart, "requestsStart"},
	{reasonOperation, "requestsOperation"},
	{reasonRebuild, "requestsRebuild"},
	{reasonResume, "requestsResume"},
}

// record adds r to the activity log in an Update of its own (7.4), which writes nothing else.
func (p *Publisher) record(ctx context.Context, r audit.Record) error {
	return p.o.Store.Update(ctx, func(tx *store.Tx) error { return tx.Record(r) })
}

// recordRequest records the request of a user before it is done, so that it is not done if the record cannot be written.
func (p *Publisher) recordRequest(ctx context.Context, r audit.Record) error {
	if err := p.record(ctx, r); err != nil {
		return fmt.Errorf("cannot record %s: %w", r.Action, err)
	}
	return nil
}

// recordEnded records what has ended, in an Update that the end of ctx does not stop and that waits for the store for
// recordTimeout at most. What has ended stays as it is if the record cannot be written, and Options.OnRecordError is told.
func (p *Publisher) recordEnded(ctx context.Context, r audit.Record) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
	defer cancel()
	if err := p.record(ctx, r); err != nil && p.o.OnRecordError != nil {
		p.o.OnRecordError(r.Action, err)
	}
}

// buildRecord returns the record of a build that took requests and has ended with res, which is nil if it published
// nothing, and err. The release that it published is the target, and Changed is that of the result. The text of the
// failure holds the problems of the site and the errors of the file system and the store, none of the accounts.
func buildRecord(requests []Request, res *build.Result, err error) audit.Record {
	detail := map[string]string{}
	for _, k := range requestKeys {
		n := 0
		for _, q := range requests {
			if q.Reason == k.reason {
				n++
			}
		}
		if n > 0 {
			detail[k.key] = strconv.Itoa(n)
		}
	}
	rec := audit.Record{Log: audit.ActivityLog, Actor: audit.System, Action: audit.Build, Detail: detail}
	if res != nil {
		rec.Target = audit.Target{Kind: audit.KindRelease, ID: res.Release}
		rec.Changed = res.Changed
		detail["pages"] = strconv.Itoa(res.Pages)
		detail["files"] = strconv.Itoa(res.Files)
		if n := len(res.Warnings); n > 0 {
			detail["warnings"] = strconv.Itoa(n)
		}
	}
	if err != nil {
		rec.Result = resultFailed
		detail["error"] = audit.Truncate(err.Error(), audit.MaxDetailValue)
	}
	return rec
}

// rollbackRecord returns the record of the user by who asks to roll back to release, which may be any text. from is the
// release that is published, if known. The release is the target if it is a name that a build makes.
func rollbackRecord(by, release, from string) audit.Record {
	rec := audit.Record{
		Log: audit.ActivityLog, Actor: by, Action: audit.ReleaseRollback,
		Detail: map[string]string{"to": audit.Truncate(release, maxReleaseName)},
	}
	if isReleaseName(release) {
		rec.Target = audit.Target{Kind: audit.KindRelease, ID: release}
	}
	if from != "" {
		rec.Detail["from"] = from
	}
	return rec
}

// isReleaseName reports whether name is 16 lower case hexadecimal digits, as the name of a release is.
func isReleaseName(name string) bool {
	if len(name) != 16 {
		return false
	}
	for i := range len(name) {
		if c := name[i]; c < '0' || c > '9' && c < 'a' || c > 'f' {
			return false
		}
	}
	return true
}

// published returns the release that the output directory publishes, "" if there is none or it cannot be told.
func (p *Publisher) published() string {
	rel, err := build.ListReleases(p.o.Out)
	if err != nil {
		return ""
	}
	return rel.Current
}
