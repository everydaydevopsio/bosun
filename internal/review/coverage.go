package review

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// CoverageSchemaVersion is bumped only for an incompatible change to Coverage.
const CoverageSchemaVersion = 1

// readsPrefix is the adapters' read-evidence control line. Like the usage line
// it is stripped from the output before anything is published.
const readsPrefix = "BOSUN_READS_V1 "

// pathListLimit bounds every path list Bosun reports, so a large change cannot
// turn a coverage summary into a file listing.
const pathListLimit = 50

// readRecord is what an adapter observed the reviewer read: repository-relative
// paths, never content. It is a lower bound -- see scripts/provider-reads.js.
type readRecord struct {
	Source   string `json:"source"`
	Method   string `json:"method"`
	Commands int    `json:"commands"`
	FullDiff bool   `json:"full_diff"`
	// DiffRanges are the revision operands of whole-diff commands, verified
	// against the merge base before FullDiffViewed is reported.
	DiffRanges []string   `json:"diff_ranges"`
	Opened     []string   `json:"opened"`
	Diffed     []pathDiff `json:"diffed"`
	Truncated  bool       `json:"truncated"`
}

// pathDiff is one `git diff <range> -- <paths>` the reviewer ran. Its paths
// are credited only once the range is verified to compare the merge base with
// HEAD; a diff of something else does not show the change under review.
type pathDiff struct {
	Range    string   `json:"range"`
	Paths    []string `json:"paths"`
	verified bool
}

// Coverage is measured evidence of how the change was examined, for a reader
// who has to decide whether a review with no findings is worth trusting (#93).
// Nothing in it is the reviewer's own claim.
type Coverage struct {
	SchemaVersion int           `json:"schema_version"`
	Diff          *DiffStats    `json:"diff"`
	DiffNote      string        `json:"diff_note,omitempty"`
	Reads         *ReadCoverage `json:"reads"`
	ReadsNote     string        `json:"reads_note,omitempty"`
}

// DiffCount sizes one side of the change. Binary files count as files with no
// line totals.
type DiffCount struct {
	Files     int `json:"files"`
	Additions int `json:"additions"`
	Deletions int `json:"deletions"`
	Binary    int `json:"binary,omitempty"`
}

// DiffStats is the size of the change from the merge base. Lockfiles,
// vendored, and generated files are counted separately: a 40,000-line lockfile
// rewrite says nothing about how much a reviewer had to read.
type DiffStats struct {
	MergeBase      string    `json:"merge_base"`
	WorkingTree    bool      `json:"working_tree"`
	Reviewable     DiffCount `json:"reviewable"`
	Generated      DiffCount `json:"generated"`
	GeneratedPaths []string  `json:"generated_paths,omitempty"`
	changed        []changedFile
	// dirty is set when a working-tree review had tracked changes beyond
	// HEAD, which no diff ending at a commit shows.
	dirty bool
}

// ReadCoverage compares what the reviewer read with what changed. Only
// reviewable files count; a deleted file has no content left to open.
type ReadCoverage struct {
	Source string `json:"source"`
	// Method is "tool_calls" when the provider has a dedicated read tool, or
	// "command_parsing" when reads are inferred from shell commands.
	Method   string `json:"method"`
	Commands int    `json:"commands"`
	// FullDiffViewed means a command printing the whole diff ran. A provider
	// may truncate long output, so it is not proof every hunk was seen.
	FullDiffViewed         bool     `json:"full_diff_viewed"`
	ChangedFiles           int      `json:"changed_files"`
	ChangedFilesOpened     int      `json:"changed_files_opened"`
	ChangedFilesDiffed     int      `json:"changed_files_diffed"`
	ChangedFilesUnexamined int      `json:"changed_files_unexamined"`
	UnexaminedPaths        []string `json:"unexamined_paths,omitempty"`
	ContextFilesOpened     int      `json:"context_files_opened"`
	Truncated              bool     `json:"truncated,omitempty"`
}

type changedFile struct {
	path      string
	additions int
	deletions int
	binary    bool
	deleted   bool
	generated bool
}

// lockfiles are rewritten wholesale by tools; their diff is not reviewed line
// by line.
var lockfiles = map[string]bool{
	"package-lock.json": true, "npm-shrinkwrap.json": true, "yarn.lock": true, "pnpm-lock.yaml": true,
	"bun.lockb": true, "bun.lock": true, "go.sum": true, "Cargo.lock": true, "poetry.lock": true,
	"Pipfile.lock": true, "uv.lock": true, "pdm.lock": true, "Gemfile.lock": true, "composer.lock": true,
	"packages.lock.json": true, "Podfile.lock": true, "mix.lock": true, "flake.lock": true,
	"pubspec.lock": true, "gradle.lockfile": true, "Package.resolved": true,
}

func generatedByName(p string) bool {
	base := path.Base(p)
	if lockfiles[base] {
		return true
	}
	for _, dir := range []string{"vendor/", "node_modules/", "third_party/"} {
		if strings.HasPrefix(p, dir) || strings.Contains(p, "/"+dir) {
			return true
		}
	}
	for _, suffix := range []string{".pb.go", "_pb2.py", "_pb2_grpc.py", ".pb.ts", ".min.js", ".min.css", ".map"} {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	return false
}

// coverageGit runs a read-only git command in the workspace. The checkout's
// own config is untrusted -- the reviewer ran there with write access just
// before -- so nothing it configures may run: hooks and fsmonitor are disabled
// here, callers pass --no-ext-diff and --no-textconv to diff, and diffStats
// refuses a working-tree comparison when clean filters are configured.
func coverageGit(ctx context.Context, dir string, stdin []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-pager", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// diffStats sizes the change the reviewer was asked to review: from the merge
// base of base (or of origin/HEAD, as the prompt does) to HEAD, plus the working
// tree and untracked files when the review read them.
func diffStats(ctx context.Context, dir, base string, workingTree bool) (*DiffStats, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if base == "" {
		out, err := coverageGit(ctx, dir, nil, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/HEAD^{commit}")
		if err != nil {
			return nil, errors.New("no base commit was given and origin/HEAD does not resolve")
		}
		base = strings.TrimSpace(string(out))
	}
	out, err := coverageGit(ctx, dir, nil, "merge-base", base, "HEAD")
	if err != nil {
		return nil, fmt.Errorf("no merge base between the base commit and HEAD: %w", err)
	}
	mergeBase := strings.TrimSpace(string(out))
	if workingTree {
		// Comparing against the working tree runs clean filters on modified
		// files. Two commits compare blob to blob and run none.
		// --includes: the diff below follows include.path, so the guard must too.
		if drivers, _ := coverageGit(ctx, dir, nil, "config", "--local", "--includes", "--name-only", "--get-regexp", `^filter\.`); len(bytes.TrimSpace(drivers)) > 0 {
			return nil, errors.New("working-tree sizing skipped: the checkout configures filter drivers, which comparing against the working tree would run")
		}
	}
	diff := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-renames", "-z"}
	tail := []string{mergeBase, "HEAD"}
	if workingTree {
		tail = []string{mergeBase}
	}
	numstat, err := coverageGit(ctx, dir, nil, append(append(append([]string{}, diff...), "--numstat"), tail...)...)
	if err != nil {
		return nil, err
	}
	deletedOut, err := coverageGit(ctx, dir, nil, append(append(append([]string{}, diff...), "--name-only", "--diff-filter=D"), tail...)...)
	if err != nil {
		return nil, err
	}
	deleted := map[string]bool{}
	for _, p := range strings.Split(string(deletedOut), "\x00") {
		if p != "" {
			deleted[p] = true
		}
	}
	var files []changedFile
	// -z numstat: "<added>\t<deleted>\t<path>\0"; binary files report "-".
	for _, entry := range strings.Split(string(numstat), "\x00") {
		parts := strings.SplitN(entry, "\t", 3)
		if len(parts) != 3 || parts[2] == "" {
			continue
		}
		f := changedFile{path: parts[2], deleted: deleted[parts[2]]}
		if parts[0] == "-" || parts[1] == "-" {
			f.binary = true
		} else {
			f.additions, _ = strconv.Atoi(parts[0])
			f.deletions, _ = strconv.Atoi(parts[1])
		}
		files = append(files, f)
	}
	if workingTree {
		untracked, err := coverageGit(ctx, dir, nil, "ls-files", "-z", "--others", "--exclude-standard")
		if err != nil {
			return nil, err
		}
		for _, p := range strings.Split(string(untracked), "\x00") {
			if p == "" {
				continue
			}
			lines, binary := countLines(filepath.Join(dir, filepath.FromSlash(p)))
			files = append(files, changedFile{path: p, additions: lines, binary: binary})
		}
	}
	if err := markGenerated(ctx, dir, files); err != nil {
		return nil, err
	}
	stats := &DiffStats{MergeBase: mergeBase, WorkingTree: workingTree, changed: files}
	if workingTree {
		uncommitted, err := coverageGit(ctx, dir, nil, append(append([]string{}, diff...), "--name-only", "HEAD")...)
		if err != nil {
			return nil, err
		}
		stats.dirty = len(bytes.TrimSpace(uncommitted)) > 0
	}
	for _, f := range files {
		side := &stats.Reviewable
		if f.generated {
			side = &stats.Generated
			if len(stats.GeneratedPaths) < pathListLimit {
				stats.GeneratedPaths = append(stats.GeneratedPaths, f.path)
			}
		}
		side.Files++
		side.Additions += f.additions
		side.Deletions += f.deletions
		if f.binary {
			side.Binary++
		}
	}
	sort.Strings(stats.GeneratedPaths)
	return stats, nil
}

// markGenerated applies the name rules and the repository's own
// linguist-generated and linguist-vendored attributes.
func markGenerated(ctx context.Context, dir string, files []changedFile) error {
	if len(files) == 0 {
		return nil
	}
	var stdin bytes.Buffer
	for i := range files {
		files[i].generated = generatedByName(files[i].path)
		stdin.WriteString(files[i].path)
		stdin.WriteByte(0)
	}
	out, err := coverageGit(ctx, dir, stdin.Bytes(), "check-attr", "-z", "--stdin", "linguist-generated", "linguist-vendored")
	if err != nil {
		return err
	}
	marked := map[string]bool{}
	// -z output: "<path>\0<attribute>\0<value>\0" per path and attribute.
	fields := strings.Split(string(out), "\x00")
	for i := 0; i+2 < len(fields); i += 3 {
		if v := fields[i+2]; v == "set" || v == "true" {
			marked[fields[i]] = true
		}
	}
	for i := range files {
		if marked[files[i].path] {
			files[i].generated = true
		}
	}
	return nil
}

// countLines counts an untracked file's lines, reporting binary content (a NUL
// in the first 8 KiB) the way git does. It never follows a symlink -- git counts
// one as a single line, and its target may be a FIFO that blocks forever or a
// file outside the repository -- and reads at most countLimit bytes.
func countLines(name string) (int, bool) {
	info, err := os.Lstat(name)
	if err != nil {
		return 0, false
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return 1, false
	}
	if !info.Mode().IsRegular() {
		return 0, false
	}
	f, err := os.Open(name)
	if err != nil {
		return 0, false
	}
	defer func() { _ = f.Close() }()
	reader := bufio.NewReader(io.LimitReader(f, countLimit))
	if head, _ := reader.Peek(8192); bytes.IndexByte(head, 0) >= 0 {
		return 0, true
	}
	lines := 0
	buf := make([]byte, 32*1024)
	last := byte('\n')
	for {
		n, err := reader.Read(buf)
		lines += bytes.Count(buf[:n], []byte{'\n'})
		if n > 0 {
			last = buf[n-1]
		}
		if err != nil {
			break
		}
	}
	if last != '\n' {
		lines++
	}
	return lines, false
}

const countLimit = 64 << 20

// revisionPattern admits the revision syntax a reviewer diffs with and
// nothing that git could read as an option.
var revisionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/@{}^~-]{0,199}$`)

// verifiedFullDiff reports whether any of the reviewer's diff ranges compared
// the merge base with HEAD, the change under review. `A...HEAD` compares the
// merge base of A and HEAD with HEAD, so A only has to share the merge base.
// `A..HEAD`, `A HEAD`, and `A` alone compare A's own tree, so A has to be the
// merge base: once the base branch moves on, `main..HEAD` also shows its
// changes reversed. `git diff HEAD`, and `HEAD~1` on a longer branch, fail.
//
// When dirty, the review read uncommitted changes, and only `A` alone -- A
// against the working tree -- shows them; a range ending at a commit does not.
func verifiedFullDiff(ctx context.Context, dir, mergeBase string, dirty bool, ranges []string) bool {
	resolve := func(rev string) string {
		if !revisionPattern.MatchString(rev) {
			return ""
		}
		out, err := coverageGit(ctx, dir, nil, "rev-parse", "--verify", "--quiet", "--end-of-options", rev+"^{commit}")
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	head := resolve("HEAD")
	if head == "" || mergeBase == "" {
		return false
	}
	for _, rng := range ranges {
		var left, right string
		threeDot := false
		fields := strings.Fields(rng)
		switch {
		case len(fields) == 2:
			left, right = fields[0], fields[1]
		case len(fields) == 1 && strings.Contains(fields[0], "..."):
			left, right, _ = strings.Cut(fields[0], "...")
			threeDot = true
		case len(fields) == 1 && strings.Contains(fields[0], ".."):
			left, right, _ = strings.Cut(fields[0], "..")
		case len(fields) == 1:
			left = fields[0]
		default:
			continue
		}
		if dirty && (right != "" || threeDot) {
			continue
		}
		if right != "" && resolve(right) != head {
			continue
		}
		a := resolve(left)
		if a == "" {
			continue
		}
		if !threeDot {
			if a == mergeBase {
				return true
			}
			continue
		}
		out, err := coverageGit(ctx, dir, nil, "merge-base", a, head)
		if err == nil && strings.TrimSpace(string(out)) == mergeBase {
			return true
		}
	}
	return false
}

// verifyReads replaces the adapter's claims with what the merge base
// supports: full_diff only for a verified whole-diff range, and path credit
// only through a verified range.
func verifyReads(ctx context.Context, dir string, stats *DiffStats, reads *readRecord) {
	mergeBase, dirty := stats.MergeBase, stats.dirty
	reads.FullDiff = verifiedFullDiff(ctx, dir, mergeBase, dirty, reads.DiffRanges)
	checked := map[string]bool{}
	for i := range reads.Diffed {
		rng := reads.Diffed[i].Range
		ok, seen := checked[rng]
		if !seen {
			ok = verifiedFullDiff(ctx, dir, mergeBase, dirty, []string{rng})
			checked[rng] = ok
		}
		reads.Diffed[i].verified = ok
	}
}

// buildCoverage joins the measured diff with the adapter's read record. Either
// half may be missing; it says so rather than reporting zeros, which would read
// as "nothing was examined".
func buildCoverage(diff *DiffStats, diffErr error, reads *readRecord) Coverage {
	c := Coverage{SchemaVersion: CoverageSchemaVersion, Diff: diff}
	if diff == nil {
		c.DiffNote = "the change could not be sized"
		if diffErr != nil {
			c.DiffNote += ": " + diffErr.Error()
		}
	}
	if reads == nil {
		c.ReadsNote = "the provider adapter reported no read evidence; files examined are unknown"
		return c
	}
	r := &ReadCoverage{Source: reads.Source, Method: reads.Method, Commands: reads.Commands,
		FullDiffViewed: reads.FullDiff, Truncated: reads.Truncated}
	c.Reads = r
	opened := setOf(reads.Opened)
	diffed := map[string]bool{}
	for _, d := range reads.Diffed {
		if d.verified {
			for _, p := range d.Paths {
				diffed[p] = true
			}
		}
	}
	if diff == nil {
		r.ContextFilesOpened = len(opened)
		c.ReadsNote = "without the diff, opened files cannot be compared with changed files"
		return c
	}
	changed := map[string]bool{}
	for _, f := range diff.changed {
		changed[f.path] = true
		// Nobody opens a binary file to review it, and a deleted file has no
		// content left to open; neither counts against coverage.
		if f.generated || f.deleted || f.binary {
			continue
		}
		r.ChangedFiles++
		switch {
		case opened[f.path]:
			r.ChangedFilesOpened++
		case diffedBy(diffed, f.path):
			r.ChangedFilesDiffed++
		default:
			r.ChangedFilesUnexamined++
			if len(r.UnexaminedPaths) < pathListLimit {
				r.UnexaminedPaths = append(r.UnexaminedPaths, f.path)
			}
		}
	}
	for p := range opened {
		if !changed[p] {
			r.ContextFilesOpened++
		}
	}
	sort.Strings(r.UnexaminedPaths)
	return c
}

// diffedBy reports whether a path's diff was requested, by its own name or by
// a directory pathspec above it.
func diffedBy(diffed map[string]bool, p string) bool {
	for {
		if diffed[p] {
			return true
		}
		i := strings.LastIndex(p, "/")
		if i < 0 {
			return false
		}
		p = p[:i]
	}
}

func setOf(paths []string) map[string]bool {
	s := make(map[string]bool, len(paths))
	for _, p := range paths {
		s[p] = true
	}
	return s
}

// Footer is the one-line coverage summary appended to a hosted review, where
// there is no `review-result` to ask.
func (c Coverage) Footer() string {
	var parts []string
	if d := c.Diff; d != nil {
		size := fmt.Sprintf("%d changed files (+%d/−%d)", d.Reviewable.Files, d.Reviewable.Additions, d.Reviewable.Deletions)
		if d.Generated.Files > 0 {
			size += fmt.Sprintf(", plus %d generated or lockfile (+%d/−%d) not counted", d.Generated.Files, d.Generated.Additions, d.Generated.Deletions)
		}
		parts = append(parts, size)
	}
	if r := c.Reads; r != nil {
		if r.FullDiffViewed {
			parts = append(parts, "full diff read")
		}
		if c.Diff != nil {
			parts = append(parts, fmt.Sprintf("%d of %d changed files opened", r.ChangedFilesOpened, r.ChangedFiles))
			if r.ChangedFilesUnexamined > 0 {
				parts = append(parts, fmt.Sprintf("%d not examined individually", r.ChangedFilesUnexamined))
			}
		}
		parts = append(parts, fmt.Sprintf("%d context files opened", r.ContextFilesOpened))
	}
	if len(parts) == 0 {
		return ""
	}
	return "<sub>Review coverage: " + strings.Join(parts, " · ") + ".</sub>"
}

// parseReadsRecord validates an adapter's read record. Every path must be
// repository-relative and clean; anything else is dropped, not repaired.
func parseReadsRecord(body []byte, source string) (*readRecord, bool) {
	var r readRecord
	if err := json.Unmarshal(body, &r); err != nil || r.Source != source || r.Commands < 0 {
		return nil, false
	}
	if r.Method != "tool_calls" && r.Method != "command_parsing" {
		r.Method = "unknown"
	}
	r.Opened = cleanPaths(r.Opened)
	var diffs []pathDiff
	for _, d := range r.Diffed {
		if len(diffs) == 50 {
			break
		}
		if d.Range != "" && len(d.Range) <= 200 {
			diffs = append(diffs, pathDiff{Range: d.Range, Paths: cleanPaths(d.Paths)})
		}
	}
	r.Diffed = diffs
	var ranges []string
	for _, rng := range r.DiffRanges {
		if len(ranges) == 20 {
			break
		}
		if len(rng) <= 200 {
			ranges = append(ranges, rng)
		}
	}
	r.DiffRanges = ranges
	return &r, true
}

func cleanPaths(paths []string) []string {
	out := []string{}
	for _, p := range paths {
		if len(out) == 5000 {
			break
		}
		if p == "" || len(p) > pathLimit || strings.ContainsAny(p, "\x00\r\n") || path.IsAbs(p) ||
			path.Clean(p) != p || p == ".." || strings.HasPrefix(p, "../") {
			continue
		}
		out = append(out, p)
	}
	return out
}
