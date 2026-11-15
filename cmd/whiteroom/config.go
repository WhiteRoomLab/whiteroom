package main

import (
	"errors"
	"flag"
	"io"

	"github.com/WhiteRoomLab/whiteroom/internal/configfile"
	"github.com/WhiteRoomLab/whiteroom/internal/content"
	"github.com/WhiteRoomLab/whiteroom/internal/dbclient"
)

// config holds the items of a config file that whiteroom uses; those that the file does not give are zero. The paths that
// the file gives are relative to its directory, so that they are the paths to open from the working directory, or absolute.
// The items of whiteroom-db, which the same file may hold, are checked by configfile and not kept.
type config struct {
	data, site, theme, out, cache string
	tmp                           string
	db                            string
	addr, socket, origin          string
	url                           string // as written: the rule of the URL is checked where it is used
	trustedProxies                []string
	workers                       int
	memory                        int64 // MiB
}

// loadConfig reads and checks the config file file, a YAML mapping of items. It reads nothing and returns the zero
// config if file is "". The error is configfile.Problems, with File set to file, for a problem in the file, and any other
// error if it cannot be read or is too large. The messages do not have the values that the file gives.
func loadConfig(file string) (config, error) {
	f, err := configfile.Load(file)
	if err != nil {
		return config{}, err
	}
	return config{
		data: f.Data, site: f.Site, theme: f.Theme, out: f.Out, cache: f.Cache, tmp: f.Tmp, db: f.DB,
		addr: f.Addr, socket: f.Socket, origin: f.Origin, url: f.URL, trustedProxies: f.TrustedProxies,
		workers: f.Workers, memory: f.Memory,
	}, nil
}

// errNoDB is the usage error of a command that needs the daemon of the store and is given none.
const errNoDB = "no database: give -db DIR, the directory of the sockets of whiteroom-db, or db in the config file"

// dbDialer is the Dialer of the sockets that whiteroom-db serve makes in the directory dir.
func dbDialer(dir string) dbclient.Dialer { return dbclient.SocketDir(dir) }

// flagGiven reports whether the command line gave the flag name, also with its default value.
func flagGiven(flags *flag.FlagSet, name string) bool {
	given := false
	flags.Visit(func(f *flag.Flag) { given = given || f.Name == name })
	return given
}

// siteURLOverride returns the URL that replaces the url of the site settings in the process of command, such as "whiteroom
// build": flagURL if it is not "", else the url of the config file, else "" for none. If it does not pass the rule of the url
// of the settings it prints on w one line, which says which of the two is wrong and why and does not have the value, and
// returns false: a usage error, with no usage text.
func siteURLOverride(w io.Writer, command, flagURL string, cfg config) (string, bool) {
	what, u := "-url", flagURL
	if u == "" {
		what, u = "url of the config file", cfg.url
	}
	if u == "" {
		return "", true
	}
	if err := content.CheckSiteURL(u); err != nil {
		printf(w, "%s: %s is not a valid site URL: %v\n", command, what, err)
		return "", false
	}
	return u, true
}

// printConfigError prints on w the error err of loadConfig for command, such as "whiteroom build": a line for each
// problem in the file, and any other error after the name of the command.
func printConfigError(w io.Writer, command string, err error) {
	var es configfile.Problems
	if !errors.As(err, &es) {
		printf(w, "%s: %v\n", command, err)
		return
	}
	for _, e := range es {
		printf(w, "%s\n", e.Error())
	}
}
