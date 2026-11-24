package docbook

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"unicode"

	"mcp-server-susedoc/internal/debug"
	"mcp-server-susedoc/internal/docparse"
)

const docModularCommit = "698631336b029563bcba107d89975e0250bab4ac"

type realCorpus struct {
	root     string
	commit   string
	corpus   *docparse.Corpus
	err      error
	warnings string
}

var (
	realOnce   sync.Once
	realLoaded *realCorpus
)

func loadReal(t *testing.T) *realCorpus {
	t.Helper()
	root := os.Getenv("SUSEDOC_TEST_DIR")
	if root == "" {
		skipOrFail(t, "SUSEDOC_TEST_DIR not set, see real_test.go")
	}
	realOnce.Do(func() {
		rc := &realCorpus{root: root}
		if out, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output(); err == nil {
			rc.commit = strings.TrimSpace(string(out))
		}
		var warn bytes.Buffer
		debug.SetOutput(&warn)
		var files []docparse.Source
		for _, p := range (Importer{}).EntryFiles(root) {
			files = append(files, docparse.Source{Path: p, Root: root})
		}
		part, err := Importer{}.Import(files)
		debug.SetOutput(os.Stderr)
		rc.err, rc.warnings = err, warn.String()
		rc.corpus = docparse.NewCorpus()
		rc.corpus.Add(part)
		rc.corpus.Finish()
		realLoaded = rc
	})
	if len(realLoaded.corpus.Documents) == 0 {
		t.Fatalf("no documents read from %s", root)
	}
	return realLoaded
}

func (rc *realCorpus) requireCommit(t *testing.T) {
	t.Helper()
	if rc.commit != docModularCommit {
		skipOrFail(t, fmt.Sprintf("checkout is at %q, the document tests need %s (see scripts/fetch-testdata.sh)", rc.commit, docModularCommit))
	}
}

func skipOrFail(t *testing.T, msg string) {
	t.Helper()
	if os.Getenv("SUSEDOC_REQUIRE_TESTDATA") != "" {
		t.Fatal(msg)
	}
	t.Skip(msg)
}

func (rc *realCorpus) doc(t *testing.T, id string) *docparse.Document {
	t.Helper()
	d := rc.corpus.Document(id)
	if d == nil {
		t.Fatalf("document %q not found", id)
	}
	return d
}

func docExtra(d *docparse.Document) *DocExtra {
	x, _ := d.Extras.(*DocExtra)
	return x
}

func chapterExtra(ch *docparse.Chapter) *ChapterExtra {
	x, _ := ch.Extra.(*ChapterExtra)
	return x
}

var (
	reLeftEntity = regexp.MustCompile(`&[A-Za-z][\w.-]*;`)
	reDocBookTag = regexp.MustCompile(`</?(?:para|simpara|command|screen|emphasis|xref|link|literal|filename|replaceable|procedure|step|itemizedlist|orderedlist|listitem|variablelist|varlistentry|note|tip|warning|important|caution|title|section|topic|info|phrase|guimenu|keycap|xi:include|co|calloutlist|table|tgroup|entry|row|imagedata|mediaobject)\b[^>]*>|\bxmlns(?::\w+)?=`)
	reCallout    = regexp.MustCompile(`<\d+>`)
)

func TestRealCorpusLoads(t *testing.T) {
	rc := loadReal(t)
	if rc.err != nil {
		t.Errorf("import errors:\n%v", rc.err)
	}
	if rc.warnings != "" {
		t.Errorf("warnings while loading:\n%s", rc.warnings)
	}
	dcs := DCFiles(rc.root)
	if len(rc.corpus.Documents) != len(dcs) {
		t.Errorf("%d documents for %d DC files", len(rc.corpus.Documents), len(dcs))
	}
}

func TestRealCorpusInvariants(t *testing.T) {
	rc := loadReal(t)
	c := rc.corpus
	l := newXMLLoader(rc.root)
	for i := range c.Documents {
		d := &c.Documents[i]
		t.Run(d.ID, func(t *testing.T) {
			xmlIDs := documentXMLIDs(t, l, rc.root, d)
			if d.Title == "" || d.Summary == "" || d.Distro.Name == "" || len(d.Distro.Versions) == 0 {
				t.Errorf("incomplete metadata: title %q, summary %q, distro %+v", d.Title, d.Summary, d.Distro)
			}
			if len(d.Chapters) == 0 {
				t.Fatal("no chapters")
			}
			if x := docExtra(d); x == nil || x.Source == nil || x.Source.Assembly == "" {
				t.Errorf("DocBook provenance missing: %+v", x)
			}
			ids := map[string]bool{}
			for j := range d.Chapters {
				ch := &d.Chapters[j]
				checkChapter(t, c, ch, xmlIDs)
				if ids[ch.ID] {
					t.Errorf("duplicate chapter id %q", ch.ID)
				}
				ids[ch.ID] = true
			}
			checkProcedureNumbers(t, d)
		})
	}
}

func documentXMLIDs(t *testing.T, l *xmlLoader, root string, d *docparse.Document) map[string]bool {
	t.Helper()
	x := docExtra(d)
	if x == nil || x.Source == nil || x.Profile == nil {
		t.Fatalf("DocBook provenance missing: %+v", x)
	}
	prof := profile{os: x.Profile.OS, arch: x.Profile.Arch, condition: x.Profile.Condition}
	ids := map[string]bool{}
	var walk func(n *xnode)
	walk = func(n *xnode) {
		if n == nil || n.name == "" {
			return
		}
		if id := n.attr["id"]; id != "" {
			ids[id] = true
		}
		for _, k := range n.kids {
			walk(k)
		}
	}
	asmPath := filepath.Join(root, x.Source.Assembly)
	asm, err := l.load(asmPath)
	if err != nil {
		t.Fatal(err)
	}
	walk(prof.apply(asm.root))
	for _, res := range asm.root.find("resource") {
		if href := res.attr["href"]; href != "" {
			f, err := l.load(filepath.Join(filepath.Dir(asmPath), href))
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				t.Errorf("resource %s: %v", href, err)
				continue
			}
			walk(prof.apply(f.root))
		}
	}
	return ids
}

func checkChapter(t *testing.T, c *docparse.Corpus, ch *docparse.Chapter, xmlIDs map[string]bool) {
	t.Helper()
	name := ch.ID
	text := c.Text(ch)
	lines := strings.Split(text, "\n")
	if ch.ID == "" || ch.Title == "" {
		t.Errorf("chapter without id or title: %q %q", ch.ID, ch.Title)
	}
	if ch.Start == 0 || ch.End < ch.Start || int(ch.End-ch.Start+1) != len(lines) {
		t.Errorf("%s: lines %d-%d, but %d lines of text", name, ch.Start, ch.End, len(lines))
	}
	if !strings.HasPrefix(lines[0], "## ") || !strings.Contains(lines[0], ch.Title) {
		t.Errorf("%s: first line %q is not the heading of %q", name, lines[0], ch.Title)
	}
	if len(strings.Fields(text)) < 10 {
		t.Errorf("%s: (almost) empty chapter: %q", name, text)
	}
	if strings.Count(text, "```")%2 != 0 {
		t.Errorf("%s: unbalanced code fences", name)
	}
	if m := reLeftEntity.FindString(text); m != "" {
		t.Errorf("%s: unexpanded entity %s", name, m)
	}
	if m := reDocBookTag.FindString(withoutCode(text)); m != "" {
		t.Errorf("%s: DocBook markup left in markdown: %s", name, m)
	}
	if strings.TrimSpace(ch.Prose) == "" {
		t.Errorf("%s: no prose", name)
	}

	x := chapterExtra(ch)
	if x == nil {
		t.Fatalf("%s: DocBook extras missing", name)
	}
	for _, s := range ch.Sections {
		if s.Title == "" {
			t.Errorf("%s: section without title: %+v", name, s)
		}
		if s.Number != "" && x.Number != "" && !strings.HasPrefix(s.Number, x.Number+".") {
			t.Errorf("%s: section %q numbered %q in chapter %q", name, s.Title, s.Number, x.Number)
		}
		if s.Number != "" && strings.Count(s.Number, ".") != s.Level {
			t.Errorf("%s: section %q numbered %q at level %d", name, s.Title, s.Number, s.Level)
		}
	}
	for _, p := range x.Procedures {
		if p.Steps == 0 {
			t.Errorf("%s: procedure %q (%s) has no steps", name, p.Title, p.ID)
		}
	}
	for _, cmd := range ch.Commands {
		if reCallout.MatchString(cmd) || reLeftEntity.MatchString(cmd) || strings.HasPrefix(cmd, "# ") || strings.HasPrefix(cmd, "> ") {
			t.Errorf("%s: command not cleaned up: %q", name, cmd)
		}
	}
	for _, linkend := range ch.Xrefs {
		if !xmlIDs[linkend] {
			t.Errorf("%s: xref to %q, which is not in the document", name, linkend)
		}
	}
	for _, img := range x.Images {
		if img.File == "" {
			t.Errorf("%s: image without file", name)
		}
	}
}

func checkProcedureNumbers(t *testing.T, d *docparse.Document) {
	t.Helper()
	next := map[string]int{}
	for j := range d.Chapters {
		x := chapterExtra(&d.Chapters[j])
		if x == nil {
			continue
		}
		for _, p := range x.Procedures {
			if p.Number == "" {
				continue
			}
			prefix, num := "", p.Number
			if i := strings.LastIndex(p.Number, "."); i >= 0 {
				prefix, num = p.Number[:i], p.Number[i+1:]
			}
			if next[prefix] == 0 {
				next[prefix] = 1
			}
			if want := fmt.Sprint(next[prefix]); num != want {
				t.Errorf("%s: procedure %q numbered %q, want %s", d.Chapters[j].ID, p.Title, p.Number, strings.TrimPrefix(prefix+"."+want, "."))
			}
			next[prefix]++
		}
	}
}

var reInlineCode = regexp.MustCompile("``[^\n]*?``|`[^`\n]*`")

func withoutCode(s string) string {
	var out []string
	fence := ""
	for _, ln := range strings.Split(s, "\n") {
		trimmed := strings.TrimLeft(ln, " >-")
		switch {
		case fence != "":
			if strings.TrimSpace(trimmed) == fence {
				fence = ""
			}
		case strings.HasPrefix(trimmed, "```"):
			fence = strings.TrimRight(trimmed, "abcdefghijklmnopqrstuvwxyz0123456789-_+ ")
		default:
			out = append(out, reInlineCode.ReplaceAllString(ln, ""))
		}
	}
	return strings.Join(out, "\n")
}

func TestRealContentCoverage(t *testing.T) {
	rc := loadReal(t)
	l := newXMLLoader(rc.root)
	for i := range rc.corpus.Documents {
		d := &rc.corpus.Documents[i]
		x := docExtra(d)
		if x == nil || x.Profile == nil {
			continue
		}
		prof := profile{os: x.Profile.OS, arch: x.Profile.Arch, condition: x.Profile.Condition}
		rendered := map[string]bool{}
		for j := range d.Chapters {
			for _, w := range coverageWords(rc.corpus.Text(&d.Chapters[j])) {
				rendered[w] = true
			}
		}
		for j := range d.Chapters {
			ch := &d.Chapters[j]
			files := []string{ch.File}
			for _, s := range ch.Sections {
				files = append(files, s.File)
			}
			seen := map[string]bool{}
			for _, file := range files {
				if file == "" || seen[file] {
					continue
				}
				seen[file] = true
				f, err := l.load(filepath.Join(rc.root, file))
				if err != nil {
					t.Errorf("%s/%s: %v", d.ID, ch.ID, err)
					continue
				}
				topic := prof.apply(f.root)
				if topic != nil {
					topic = &xnode{name: topic.name, attr: topic.attr, kids: slices.DeleteFunc(slices.Clone(topic.kids), func(k *xnode) bool { return k.name == "info" })}
				}
				words := map[string]bool{}
				sourceWords(topic, words)
				var missing []string
				for w := range words {
					if !covered(w, rendered) {
						missing = append(missing, w)
					}
				}
				if len(missing) > 0 {
					sort.Strings(missing)
					t.Errorf("%s/%s: %d of %d words of %s not rendered: %q", d.ID, ch.ID, len(missing), len(words), file, missing[:min(len(missing), 15)])
				}
			}
		}
	}
}

func sourceWords(n *xnode, words map[string]bool) {
	if n == nil {
		return
	}
	if n.name == "" {
		for _, w := range coverageWords(n.text) {
			words[w] = true
		}
		return
	}
	switch n.name {
	case "remark", "indexterm", "co", "meta", "revhistory", "docmanager":
		return
	}
	for _, k := range n.kids {
		sourceWords(k, words)
	}
}

func covered(w string, rendered map[string]bool) bool {
	if rendered[w] {
		return true
	}
	for r := range rendered {
		if strings.HasPrefix(r, w) || strings.HasSuffix(r, w) {
			return true
		}
	}
	return false
}

func coverageWords(s string) []string {
	seen := map[string]bool{}
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len(w) >= 3 && !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	return out
}

func chapterIn(t *testing.T, d *docparse.Document, id string) *docparse.Chapter {
	t.Helper()
	ch := d.Chapter(id)
	if ch == nil {
		t.Fatalf("%s: chapter %q not found", d.ID, id)
	}
	return ch
}

func sectionList(ch *docparse.Chapter) []string {
	var out []string
	for _, s := range ch.Sections {
		out = append(out, fmt.Sprintf("%s %s L%d", s.Number, s.ID, s.Level))
	}
	return out
}

func assertEqual[T any](t *testing.T, what string, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s:\ngot  %#v\nwant %#v", what, got, want)
	}
}

func TestRealDocuments(t *testing.T) {
	rc := loadReal(t)
	rc.requireCommit(t)
	c := rc.corpus

	t.Run("kdump profiles", func(t *testing.T) {
		sles, sap := rc.doc(t, "sles-kdump"), rc.doc(t, "sap-kdump")
		for _, d := range []*docparse.Document{sles, sap} {
			assertEqual(t, d.ID+" title", d.Title, "Kdump Configuration and Troubleshooting Guide")
			x := docExtra(d)
			assertEqual(t, d.ID+" source", *x.Source, DocSource{DC: "DC-" + map[string]string{"sles-kdump": "SLES", "sap-kdump": "SAP"}[d.ID] + "-kdump", Assembly: "articles/kdump.asm.xml", Structure: "kdump-basics"})
			var ids, nums, types []string
			for i := range d.Chapters {
				ids = append(ids, d.Chapters[i].ID)
				nums = append(nums, chapterExtra(&d.Chapters[i]).Number)
				types = append(types, chapterExtra(&d.Chapters[i]).TopicType)
			}
			assertEqual(t, d.ID+" chapters", ids, []string{"about-kdump", "configure-kdump", "troubleshoot-kdump", "more-info-kdump"})
			assertEqual(t, d.ID+" numbers", nums, []string{"1", "2", "3", "4"})
			assertEqual(t, d.ID+" topic types", types, []string{"concept", "task", "task", "glue"})
			ts := chapterIn(t, d, "troubleshoot-kdump")
			assertEqual(t, d.ID+" troubleshoot sections", sectionList(ts), []string{"3.1 troubleshooting-kdump L1", "3.1.1 related-termination-signals-systemd L2"})
			assertEqual(t, d.ID+" troubleshoot procedures", chapterExtra(ts).Procedures, []Procedure{{Steps: 4}})
		}

		assertEqual(t, "sles distro", sles.Distro, docparse.Distro{Name: "SUSE Linux Enterprise Server", Versions: []string{"16.1"}})
		assertEqual(t, "sap distro", sap.Distro, docparse.Distro{Name: "SUSE Linux Enterprise Server for SAP applications", Versions: []string{"16.1"}})
		assertEqual(t, "sles profile", *docExtra(sles).Profile, Profile{OS: []string{"sles"}, Condition: []string{"suse-product"}})

		slesCfg, sapCfg := chapterIn(t, sles, "configure-kdump"), chapterIn(t, sap, "configure-kdump")
		assertEqual(t, "sles sections", sectionList(slesCfg), []string{"2.1 kdump-non-transact L1", "2.2 kdump-transact L1", "2.3 testing-kdump L1"})
		assertEqual(t, "sap sections", sectionList(sapCfg), []string{"2.1 kdump-non-transact L1", "2.2 testing-kdump L1"})
		assertEqual(t, "sles commands", slesCfg.Commands, []string{
			"sudo transactional-update pkg install kdump",
			"sudo zypper install kdump",
			"sudo journalctl -u kdump-commandline.service",
			"sudo transactional-update setup-kdump [--crashkernel=low,high]",
		})
		assertEqual(t, "sap commands", sapCfg.Commands, []string{"sudo zypper install kdump", "sudo journalctl -u kdump-commandline.service"})
		slesText, sapText := c.Text(slesCfg), c.Text(sapCfg)
		for _, s := range []string{"To install Kdump on the immutable system, run the following command and reboot:", "To install Kdump on the mutable system, run the following command:", "## 2.2 Configuring Kdump for immutable systems {#kdump-transact}"} {
			if !strings.Contains(slesText, s) {
				t.Errorf("sles configure-kdump lacks %q", s)
			}
			if strings.Contains(sapText, s) {
				t.Errorf("sap configure-kdump has the SLES only text %q", s)
			}
		}
		if !strings.Contains(sapText, "To install Kdump, run the following command:") {
			t.Errorf("sap configure-kdump lacks the profiled install paragraph")
		}
	})

	t.Run("snapper", func(t *testing.T) {
		d := rc.doc(t, "sles-snapper-basics")
		concept := chapterIn(t, d, "snapper-basic-concept")
		assertEqual(t, "sections", sectionList(concept), []string{"2.1 snapper-basic-tasks L1", "2.2 snapshot-types L1", "2.3 snapper-snapshot-creation L1"})
		want := "**Snapshot types based on the time of creation**\n\nAmong administration and installation snapshots, Snapper recognizes three different types: pre, post and single. These do not differ physically, but Snapper handles them differently.\n\n- **`pre`**"
		if text := c.Text(concept); !strings.Contains(text, want) {
			t.Errorf("variablelist introduction missing, want\n%s", want)
		}
		setup := chapterIn(t, d, "snapper-default-setup")
		assertEqual(t, "xref targets", c.XrefTargets(d.ID, setup), []docparse.XrefTarget{
			{Linkend: "snapper-subvolumes", Text: "Subvolumes excluded from snapshots", Resolved: true, DocID: d.ID, ChapterID: "snapper-default-setup", SectionID: "snapper-subvolumes", Title: "Subvolumes excluded from snapshots", Scope: "section"},
			{Linkend: "snapshot-types", Text: "Types of snapshots", Resolved: true, DocID: d.ID, ChapterID: "snapper-basic-concept", SectionID: "snapshot-types", Title: "Types of snapshots", Scope: "section"},
		})
	})

	t.Run("sbd watchdog", func(t *testing.T) {
		d := rc.doc(t, "ha-sbd-configuring-diskbased")
		assertEqual(t, "distro", d.Distro, docparse.Distro{Name: "SUSE Linux Enterprise High Availability", Versions: []string{"16.1"}})
		assertEqual(t, "profile", *docExtra(d).Profile, Profile{OS: []string{"sleha"}, Condition: []string{"sbd-diskbased"}})
		ch := chapterIn(t, d, "ha-sbd-setting-up-watchdog")
		x := chapterExtra(ch)
		assertEqual(t, "procedures", x.Procedures, []Procedure{{Steps: 8}})
		assertEqual(t, "includes", x.Includes, []string{"snippets/ha-watchdog.xml"})
		for _, cmd := range []string{
			`sudo bash -c "echo WATCHDOG_MODULE > /etc/modules-load.d/watchdog.conf"`,
			`echo WATCHDOG_MODULE > /etc/modules-load.d/watchdog.conf`,
			`sudo sbd -w /dev/WATCHDOG_DEVICE test-watchdog`,
		} {
			if !slices.Contains(ch.Commands, cmd) {
				t.Errorf("command %q missing from %q", cmd, ch.Commands)
			}
		}
		assertEqual(t, "xrefs", ch.Xrefs, []string{"sbd-watchdog-device-verify", "ha-watchdog-drivers"})
		if text := c.Text(ch); !strings.Contains(text, "see [Commonly used watchdog drivers](#ha-watchdog-drivers)") {
			t.Error("xref to the table does not show its title")
		}
	})
}
