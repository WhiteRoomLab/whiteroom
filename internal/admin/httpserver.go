package admin

import (
	"io"
	"log"
	"net/http"
	"time"
)

// writeTimeout is how long the response of a request has to be sent, from the time that the request is read.
const writeTimeout = 60 * time.Second

// NewHTTPServer returns an http.Server for h with the limits of the admin server. It marks the connections over Unix
// domain sockets, whose peers Server trusts as proxies. errorLog gets the errors of net/http itself; nil discards them.
func NewHTTPServer(h http.Handler, errorLog *log.Logger) *http.Server {
	if errorLog == nil {
		errorLog = log.New(io.Discard, "", 0)
	}
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// The public server has no WriteTimeout, for the large files that it sends. Responses here are small; the routes
		// of the heavy requests extend their own deadlines with http.ResponseController (extendRead and extendWrite).
		WriteTimeout:                 writeTimeout,
		IdleTimeout:                  120 * time.Second,
		MaxHeaderBytes:               65536,
		DisableGeneralOptionsHandler: true,
		ErrorLog:                     errorLog,
		ConnContext:                  markUnix,
	}
}
