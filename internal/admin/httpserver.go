package admin

import (
	"io"
	"log"
	"net/http"
	"time"
)

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
		// The public server has no WriteTimeout, for the large files that it sends. Responses here are small; the ones
		// that are not (#48) extend their own deadline with http.ResponseController.
		WriteTimeout:                 60 * time.Second,
		IdleTimeout:                  120 * time.Second,
		MaxHeaderBytes:               65536,
		DisableGeneralOptionsHandler: true,
		ErrorLog:                     errorLog,
		ConnContext:                  markUnix,
	}
}
