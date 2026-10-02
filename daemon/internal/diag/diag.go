// Package diag carries a component's log lines to the writer main chose, each
// line tagged with the name of the component that wrote it.
package diag

// This file is the logging seam. It exists because the standard library's log
// package is process-wide: the shortest path at every call site is a direct
// call, which leaves nothing to say where a component's lines belong, and no
// way for a test to hold them without swapping the global output and racing
// every other test in the binary.
//
// The interface is one method wide on purpose. A component has no say in how
// its lines are shaped -- not the prefix, not the timestamp, not where they
// end up -- so it carries the message and nothing else, and New is the single
// place any of the rest is decided.

import (
	"io"
	"log"
)

// Logger is where one component writes its lines.
type Logger interface {
	// Printf writes one line, with format and args as log.Printf takes them.
	Printf(format string, args ...any)
}

// New returns a Logger that writes component's lines to w, each line prefixed
// with the component's name.
//
// The flags are the standard library's default rather than none. journald
// stamps every line it receives, but this output also reaches a terminal when
// the daemon is started by hand, and there a timestamp is the difference
// between a report and a mystery.
//
// Lmsgprefix puts that timestamp before the component rather than after it, so
// a line reads as a time, then a name, then what it said. Without it the
// timestamp lands between the name and the message it names, and both are
// harder to read for it.
func New(w io.Writer, component string) Logger {
	return text{logger: log.New(w, component+": ", log.LstdFlags|log.Lmsgprefix)}
}

// Discard returns a Logger that drops every line. It is what the --check report
// hands the probes it builds: the report is what that path prints, and a probe
// narrating its own progress would land in the journal beside a report already
// saying the same thing.
func Discard() Logger { return discard{} }

// text writes through the standard library logger, which is what applies the
// prefix and the timestamp it was built with.
type text struct {
	logger *log.Logger
}

// Printf writes one line through the standard library logger, which is what
// applies the prefix and the timestamp this Logger was built with.
func (t text) Printf(format string, args ...any) {
	t.logger.Printf(format, args...)
}

// discard writes nothing.
type discard struct{}

// Printf drops one line.
func (discard) Printf(string, ...any) {}
