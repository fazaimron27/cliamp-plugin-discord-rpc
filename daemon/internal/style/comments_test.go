package style_test

// This file is the guard for the comment convention written out in
// docs/comments.md. It reads the repository's own Go files rather than its own
// behaviour, so it fails when the convention drifts back out of the tree, and
// the failure names the file and line — which is what makes a convention a
// convention rather than a preference.
//
// The rules it enforces, and what each is for:
//
//   - Every file opens with a purpose comment, placed after the package clause.
//     It has to sit below the clause rather than above it: a comment above the
//     clause becomes a package doc, and go/doc concatenates the package docs of
//     every file in a package, so per-file headers there would turn the package
//     documentation into a jumble of file descriptions. Below the clause the
//     comment is invisible to go/doc and is still the first thing a reader meets.
//   - No comment inside a function body. The reasoning that would have lived
//     there belongs in the file's purpose comment or in the doc comment of the
//     declaration it explains, which is why this rule and the one above are two
//     halves of one decision rather than two preferences.
//   - Every package has a package doc, opening with Package <name> — or
//     Command <binary> for package main, which is named for the binary it builds
//     and not for the clause every main package shares.
//   - Every exported declaration carries a doc comment that opens with its
//     identifier. Test files are outside this rule and the one after it: a test
//     file's exports are its own business, and a doc comment there describes the
//     scenario rather than restating the test's name.
//   - Comment prose fits 80 columns, counted from the marker rather than from
//     the line's start, so an indented comment is held to the same width as a
//     top-level one.
//
// Two exemptions come from reading the tree rather than from taste. A grouped
// const or var block may be documented once for the block or once per spec —
// config.go does the first, statewatch.go the second — so requiring both would
// flag a dozen legitimate lines. And a doc comment on a struct field is not a
// declaration comment, so fields stay out of scope.
//
// Two parts are deliberately left to review, because no syntax tree can judge
// them: whether a trailing comment annotates a value or explains logic, and
// whether a single-line sentence ends in a period, which a comment ending in a
// URL or a code fragment legitimately does not.
//
// The walk is anchored the way release_test.go anchors its own: Go runs a test
// binary with its working directory set to the package directory, so this
// package sits two levels below the daemon tree it covers.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// sourceRoot is the tree the convention covers.
const sourceRoot = "../.."

// maxCommentColumn is the bound the convention puts on comment prose. It counts
// the text after the marker rather than the whole line, so an indented comment
// is held to the same width as a top-level one; a line's total width varies with
// the code beside it, which is not what the rule is about.
const maxCommentColumn = 80

// source is one parsed file: the syntax the declaration rules read, plus the
// raw text and file set the column bound and error positions read.
type source struct {
	path string
	text string
	fset *token.FileSet
	file *ast.File
}

// site is one place the convention applies: a declaration that has to carry a
// doc comment and open it with its own name.
type site struct {
	source source
	line   int
	name   string
	doc    *ast.CommentGroup
	// group records that the comment introducing this name belongs to the
	// enclosing const or var block rather than to the name itself. Go lets a
	// block be documented once, for every name in it, so such a comment speaks
	// for the block and cannot be expected to open with any one identifier.
	group bool
}

// TestCommentConvention is the guard. Each subtest is one rule, so a failure
// says which rule a site broke as well as where it is.
func TestCommentConvention(t *testing.T) {
	sources := parseDaemon(t)
	sites := collectSites(t, sources)

	t.Run("file purpose comment", func(t *testing.T) {
		for _, s := range sources {
			if purposeComment(s) == nil {
				t.Errorf("%s: no purpose comment after the package clause; every file opens with one", s.path)
			}
		}
	})

	t.Run("no comments inside function bodies", func(t *testing.T) {
		for _, s := range sources {
			for _, comment := range bodyComments(s) {
				line := s.fset.Position(comment.Pos()).Line
				t.Errorf("%s:%d: comment inside a function body; move what it says into the file's purpose comment or the declaration's doc comment: %s",
					s.path, line, firstLine(comment.Text()))
			}
		}
	})

	t.Run("package doc", func(t *testing.T) {
		checkPackageDocs(t, sources)
	})

	t.Run("exported declarations are documented", func(t *testing.T) {
		for _, s := range sites {
			if s.doc == nil {
				t.Errorf("%s:%d: %s is exported and has no doc comment", s.source.path, s.line, s.name)
			}
		}
	})

	t.Run("doc comments open with the identifier", func(t *testing.T) {
		for _, s := range sites {
			if s.doc == nil || s.group {
				continue
			}
			if first := firstWord(s.doc.Text()); first != s.name {
				t.Errorf("%s:%d: %s doc comment opens with %q, want the name", s.source.path, s.line, s.name, first)
			}
		}
	})

	t.Run("comment prose fits the column bound", func(t *testing.T) {
		checkColumnBound(t, sources)
	})
}

// parseDaemon reads every Go file under sourceRoot. The walk skips testdata,
// whose fixtures are not source, and dot directories, which hold none either.
//
// A walk that found nothing would satisfy every rule by having nothing to check,
// which is the one way this guard could pass while the convention rots.
// release_test.go fails the same way when a file it scans loses its pin, and the
// Lua contract harness fails when a scenario publishes nothing.
func parseDaemon(t *testing.T) []source {
	t.Helper()
	var sources []source
	err := filepath.WalkDir(sourceRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != sourceRoot && (entry.Name() == "testdata" || strings.HasPrefix(entry.Name(), ".")) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, data, parser.ParseComments)
		if err != nil {
			return err
		}
		sources = append(sources, source{path: path, text: string(data), fset: fset, file: file})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) == 0 {
		t.Fatalf("no Go files found under %s, so nothing was checked", sourceRoot)
	}
	return sources
}

// purposeComment is the comment that opens a file for a reader: the first one
// after the package clause. A package doc sits above the clause and does not
// count, because go/doc reads it as package documentation rather than as a
// description of this file.
//
// The comment has to stand on its own. A comment the parser attaches as the doc
// of the first declaration documents that declaration instead, which is why a
// header without a blank line under it does not count — and a file that declares
// nothing has no first declaration to be bounded by, so it needs only to carry a
// comment after the clause.
func purposeComment(s source) *ast.CommentGroup {
	for _, group := range s.file.Comments {
		if group.Pos() <= s.file.Name.End() {
			continue
		}
		if len(s.file.Decls) == 0 {
			return group
		}
		if first := s.file.Decls[0]; group.Pos() >= first.Pos() || group == declDoc(first) {
			return nil
		}
		return group
	}
	return nil
}

// declDoc is the doc comment of a declaration, or nil for one that carries
// none. The Doc field is not on the Decl interface, so it takes a type switch.
func declDoc(decl ast.Decl) *ast.CommentGroup {
	switch d := decl.(type) {
	case *ast.GenDecl:
		return d.Doc
	case *ast.FuncDecl:
		return d.Doc
	}
	return nil
}

// bodyComments lists every comment block sitting inside a function body,
// whether the function is declared or a literal. This is the rule that keeps
// reasoning out of the middle of an implementation: what a bare comment there
// does is explain the line under it, and an explanation that only makes sense
// beside one line is the kind that goes stale without anything noticing.
//
// A block is recorded as a block rather than line by line, because a block is
// what moves. A block nested inside another body — a closure inside a declared
// function, most often — matches both bodies and is still recorded once, so the
// count a run reports is a count of blocks rather than of nesting depth.
func bodyComments(s source) []*ast.CommentGroup {
	var bodies []*ast.BlockStmt
	ast.Inspect(s.file, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.FuncDecl:
			if n.Body != nil {
				bodies = append(bodies, n.Body)
			}
		case *ast.FuncLit:
			bodies = append(bodies, n.Body)
		}
		return true
	})

	seen := map[*ast.CommentGroup]bool{}
	var found []*ast.CommentGroup
	for _, body := range bodies {
		for _, group := range s.file.Comments {
			if group.Pos() > body.Lbrace && group.End() < body.Rbrace && !seen[group] {
				seen[group] = true
				found = append(found, group)
			}
		}
	}
	return found
}

// collectSites names every declaration the convention applies to: the exported
// ones in files that ship. Test files are left out, because a test's exports are
// its own business — the convention asks for a scenario description there, not a
// doc comment opening with the name.
func collectSites(t *testing.T, sources []source) []site {
	t.Helper()
	var sites []site
	for _, s := range sources {
		if isTest(s.path) {
			continue
		}
		for _, decl := range s.file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if ast.IsExported(d.Name.Name) {
					sites = append(sites, site{
						source: s,
						line:   s.fset.Position(d.Pos()).Line,
						name:   d.Name.Name,
						doc:    d.Doc,
					})
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					sites = append(sites, specsOf(s, d, spec)...)
				}
			}
		}
	}
	if len(sites) == 0 {
		t.Fatalf("no exported declarations found under %s, so nothing was checked", sourceRoot)
	}
	return sites
}

// specsOf expands one const, var, or type spec into the exported names it
// declares. A spec may declare several names at once, and each is a name the
// convention applies to; the comment that covers them is the spec's own if it
// has one, and otherwise the block's.
//
// A parenthesized block is the only shape whose comment can belong to the block.
// The parser hands a bare declaration's comment to the GenDecl, so a single
// `const Number = ...` arrives with its own doc on the group and none on the
// spec; reading that as a block comment would exempt every unparenthesized
// constant from the identifier rule, which is most of the declarations the
// convention has to reach.
func specsOf(s source, decl *ast.GenDecl, spec ast.Spec) []site {
	block := len(decl.Specs) > 1
	switch v := spec.(type) {
	case *ast.TypeSpec:
		if !ast.IsExported(v.Name.Name) {
			return nil
		}
		return []site{documentedBy(s, v.Name, v.Doc, decl.Doc, block)}
	case *ast.ValueSpec:
		var out []site
		for _, name := range v.Names {
			if ast.IsExported(name.Name) {
				out = append(out, documentedBy(s, name, v.Doc, decl.Doc, block))
			}
		}
		return out
	}
	return nil
}

// documentedBy pairs a name with the comment introducing it. The spec's own
// comment wins where it has one; where it does not, the block's comment covers
// it, and the result is marked as a group comment so the identifier rule knows
// not to read a block-wide sentence as this name's opening words. Both forms are
// in the tree: config.go documents a whole transport block once, and
// statewatch.go documents each spec inside its block and leaves the block itself
// uncommented.
func documentedBy(s source, name *ast.Ident, own, group *ast.CommentGroup, block bool) site {
	doc, fromGroup := own, false
	if doc == nil {
		doc, fromGroup = group, block
	}
	return site{
		source: s,
		line:   s.fset.Position(name.Pos()).Line,
		name:   name.Name,
		doc:    doc,
		group:  fromGroup,
	}
}

// checkPackageDocs requires a doc comment on every package, opening with the
// package clause for a library and with Command for a main package.
//
// One package is counted per directory per clause: an external test package
// (foo_test) shares its directory with the package it tests, and is not a
// package the convention documents.
func checkPackageDocs(t *testing.T, sources []source) {
	t.Helper()
	documented := map[string]bool{}
	for _, s := range sources {
		if isTest(s.path) || s.file.Doc == nil {
			continue
		}
		dir, name := filepath.Dir(s.path), s.file.Name.Name
		if opensPackageDoc(s.file.Doc.Text(), name, filepath.Base(dir)) {
			documented[dir+"\x00"+name] = true
		}
	}

	seen := map[string]bool{}
	for _, s := range sources {
		if isTest(s.path) {
			continue
		}
		dir, name := filepath.Dir(s.path), s.file.Name.Name
		key := dir + "\x00" + name
		if seen[key] {
			continue
		}
		seen[key] = true
		if !documented[key] {
			t.Errorf("%s: package %s has no doc comment opening with %q", dir, name, packageWord(name))
		}
	}
}

// packageWord is the word a package's doc comment opens with.
func packageWord(name string) string {
	if name == "main" {
		return "Command <binary>"
	}
	return "Package " + name
}

// opensPackageDoc reports whether a package doc comment is written the way the
// convention asks.
func opensPackageDoc(text, name, dir string) bool {
	fields := strings.Fields(firstLine(text))
	if len(fields) < 2 {
		return false
	}
	if name == "main" {
		return fields[0] == "Command" && fields[1] == dir
	}
	return fields[0] == "Package" && fields[1] == name
}

// checkColumnBound holds every comment line, on every file, to the width the
// convention sets. It reads the syntax tree's comments rather than searching
// each line for `//`, which would find the marker inside a URL string literal
// and measure the rest of the line as prose.
func checkColumnBound(t *testing.T, sources []source) {
	t.Helper()
	for _, s := range sources {
		for _, group := range s.file.Comments {
			for _, comment := range group.List {
				text, ok := lineComment(comment.Text)
				if !ok {
					continue
				}
				if width := utf8.RuneCountInString(text); width > maxCommentColumn {
					line := s.fset.Position(comment.Pos()).Line
					t.Errorf("%s:%d: comment runs to %d columns, past %d", s.path, line, width, maxCommentColumn)
				}
			}
		}
	}
}

// lineComment returns the prose a `//` comment carries. A block comment is
// reported as absent: it has its own shape, and the convention's column bound is
// written for the line form this repository uses.
func lineComment(raw string) (string, bool) {
	text, ok := strings.CutPrefix(raw, "//")
	if !ok {
		return "", false
	}
	return strings.TrimPrefix(text, " "), true
}

// firstWord is the identifier a doc comment is expected to open with.
func firstWord(text string) string {
	fields := strings.Fields(firstLine(text))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// firstLine is the opening line of a comment or doc block, which is the line the
// convention's opening rules read.
func firstLine(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	return line
}

// isTest reports whether a path is a test file.
func isTest(path string) bool {
	return strings.HasSuffix(path, "_test.go")
}
