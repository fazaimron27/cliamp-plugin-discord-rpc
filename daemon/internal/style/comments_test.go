package style_test

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

// sourceRoot is the tree the convention covers. Go runs a test binary with its
// working directory set to the package directory, so this package sits two
// levels below the daemon tree it walks.
const sourceRoot = "../.."

// maxCommentColumn is the bound the convention puts on comment prose. It counts
// the text after the marker rather than the whole line, so an indented comment
// is held to the same width as a top-level one; a line's total width varies
// with the code beside it, which is not what the rule is about.
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

// TestCommentConvention is the guard for the comment convention written out in
// docs/comments.md. It reads the repository's own Go files rather than its own
// behaviour, so it fails when the convention drifts back out of the tree — the
// failure names the file and line, which is what makes it a convention instead
// of a preference.
//
// The rules it enforces are the mechanical ones. Two parts of the convention
// are deliberately left to review because they cannot be read off a syntax
// tree: whether a trailing comment annotates a value or explains logic, and
// whether a single-line sentence ends in a period, which a comment ending in a
// URL or a code fragment legitimately does not.
func TestCommentConvention(t *testing.T) {
	sources := parseDaemon(t)
	sites := collectSites(t, sources)

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
// whose fixtures are not source, and dot directories, which hold no source
// either.
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
	// A walk that found nothing would satisfy every rule below by having
	// nothing to check, which is the one way this guard could pass while the
	// convention rots. release_test.go fails the same way when a file it scans
	// loses its pin, and the Lua contract harness fails when a scenario
	// publishes nothing.
	if len(sources) == 0 {
		t.Fatalf("no Go files found under %s, so nothing was checked", sourceRoot)
	}
	return sources
}

// collectSites names every declaration the convention applies to: the exported
// ones in files that ship. Test files are left out, because a test's exports
// are its own business — the convention asks for a scenario description there,
// not a doc comment opening with the name.
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
func specsOf(s source, decl *ast.GenDecl, spec ast.Spec) []site {
	// A parenthesized block is the only shape whose comment can belong to the
	// block: the parser hands a bare declaration's comment to the GenDecl, so
	// a single `const Number = ...` arrives with its own doc on the group and
	// none on the spec. Reading that as a block comment would exempt every
	// unparenthesized constant from the identifier rule — which is most of the
	// declarations the convention has to reach.
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
// not to read a block-wide sentence as this name's opening words. Both forms
// are in the tree: config.go documents a whole transport block once, and
// statewatch.go documents each spec inside its block and leaves the block
// itself uncommented.
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
// package clause for a library and with Command for a main package, which is
// named for the binary it builds rather than for the clause every main package
// shares.
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

	// One package per directory per clause: an external test package
	// (foo_test) shares its directory with the package it tests but is not a
	// package the convention documents.
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
// reported as absent: it has its own shape, and the convention's column bound
// is written for the line form this repository uses.
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

// firstLine is the opening line of a comment or doc block, which is the line
// the convention's opening rules read.
func firstLine(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	return line
}

// isTest reports whether a path is a test file.
func isTest(path string) bool {
	return strings.HasSuffix(path, "_test.go")
}
