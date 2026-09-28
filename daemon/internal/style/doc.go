// Package style holds this repository's comment convention: the rules the test
// beside this file enforces across every Go file, and the reason the package
// exists at all.
//
// It has no API. The convention belongs to no single package, so it needs a
// home that is not pretending to own the check, and a package named for the
// convention is that home. The alternative — hanging the guard off the package
// whose comments were last found wanting — would move it on the next change.
//
// The convention is written for contributors in docs/comments.md. This package
// exists so a Go reader meets it where the guard that enforces it lives.
package style
