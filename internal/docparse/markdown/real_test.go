package markdown

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcp-server-susedoc/internal/docparse"
)

func TestRealTrees(t *testing.T) {
	dirs := filepath.SplitList(os.Getenv("SUSEDOC_MD_TEST_DIRS"))
	if len(dirs) == 0 {
		if os.Getenv("SUSEDOC_REQUIRE_TESTDATA") != "" {
			t.Fatal("SUSEDOC_MD_TEST_DIRS not set, see real_test.go")
		}
		t.Skip("SUSEDOC_MD_TEST_DIRS not set, see real_test.go")
	}
	for _, dir := range dirs {
		t.Run(filepath.Base(filepath.Dir(dir))+"/"+filepath.Base(dir), func(t *testing.T) {
			var srcs []docparse.Source
			err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() && p != dir && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules") {
					return filepath.SkipDir
				}
				if !d.IsDir() && (Importer{}).Match(p) {
					srcs = append(srcs, docparse.Source{Path: p, Root: dir})
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(srcs) == 0 {
				t.Fatalf("no markdown files in %s", dir)
			}
			part, err := Importer{}.Import(srcs)
			if err != nil {
				t.Errorf("import: %v", err)
			}
			c := docparse.NewCorpus()
			c.Add(part)
			c.Finish()

			covered := map[string]map[int]bool{}
			for i := range c.Documents {
				d := &c.Documents[i]
				if d.Title == "" && len(d.Chapters) > 0 {
					t.Errorf("%s: document without title", d.ID)
				}
				for j := range d.Chapters {
					ch := &d.Chapters[j]
					if strings.TrimSpace(ch.Title) == "" || strings.Contains(ch.Title, "{#") {
						t.Errorf("%s/%s: bad chapter title %q in %s", d.ID, ch.ID, ch.Title, ch.File)
					}
					if covered[ch.File] == nil {
						covered[ch.File] = map[int]bool{}
					}
					for l := ch.Start; l <= ch.End; l++ {
						covered[ch.File][int(l)] = true
					}
				}
			}
			for _, src := range srcs {
				rel, err := filepath.Rel(dir, src.Path)
				if err != nil {
					t.Fatal(err)
				}
				if lost := lostLines(t, src.Path, covered[filepath.ToSlash(rel)]); len(lost) > 0 {
					t.Errorf("%s: %d lines of content in no chapter, e.g. %q", src.Path, len(lost), lost[:min(len(lost), 3)])
				}
			}
		})
	}
}
