package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"

	"github.com/WhiteRoomLab/whiteroom/internal/publish"
	"github.com/WhiteRoomLab/whiteroom/internal/store"
)

// buildFromStore runs whiteroom build for the data directory data as s says, and returns the exit code. The data directory
// is opened as a process that reads it, and stays open until the build has ended, which reads the revisions and the
// originals from it. From the site directory s.site it reads only the schema file and the theme.
func buildFromStore(ctx context.Context, stdout io.Writer, p errorPrinter, data string, s buildSettings) int {
	p.stored = true
	schema, themeFS, closeSite, err := openAdminSite(s.site, s.theme)
	if err != nil {
		p.printError(ctx, err)
		return 1
	}
	defer closeSite()
	st, err := store.OpenReader(data)
	if err != nil {
		p.printError(ctx, err)
		return 1
	}
	defer func() { _ = st.Close() }()
	src, err := publish.Snapshot(ctx, st, schema, themeFS)
	if err != nil {
		p.printError(ctx, err)
		return 1
	}
	// Not left to Build, which makes the output and the cache directories before it reads the settings.
	if _, _, err := src.Settings(); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			err = fmt.Errorf("the data directory %s has no site settings: whiteroom init imports those of a site, and the admin "+
				"API saves them (PUT /api/settings)", data)
		}
		p.printError(ctx, err)
		return 1
	}
	return buildAndReport(ctx, stdout, p, src, s)
}
