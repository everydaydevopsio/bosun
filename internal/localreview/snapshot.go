package localreview

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	b, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %s", args[0], strings.TrimSpace(string(b)))
	}
	return strings.TrimSpace(string(b)), nil
}
func resolve(ctx context.Context, repo, ref string) (string, error) {
	return git(ctx, repo, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
}

type Snapshot struct {
	Path, Repo, Branch, Head, Base string
	Changes                        int
	Committed                      bool
}

func snapshot(ctx context.Context, path, branch, base string) (s Snapshot, err error) {
	path, err = filepath.Abs(path)
	if err != nil {
		return
	}
	s.Repo, err = git(ctx, path, "rev-parse", "--show-toplevel")
	if err != nil {
		return
	}
	s.Committed = branch != ""
	sourceHead, e := resolve(ctx, s.Repo, "HEAD")
	if e != nil {
		return s, e
	}
	before, e := git(ctx, s.Repo, "status", "--porcelain=v1", "--untracked-files=all")
	if e != nil {
		return s, e
	}
	indexPath, e := git(ctx, s.Repo, "rev-parse", "--path-format=absolute", "--git-path", "index")
	if e != nil {
		return s, e
	}
	indexBefore, _ := os.ReadFile(indexPath)
	beforeHash := sha256.Sum256(indexBefore)
	if branch == "" {
		branch, _ = git(ctx, s.Repo, "symbolic-ref", "--short", "-q", "HEAD")
		if branch == "" {
			branch = sourceHead
		}
	}
	s.Branch = branch
	s.Head, err = resolve(ctx, s.Repo, branch)
	if err != nil {
		return
	}
	if base == "" {
		base, _ = git(ctx, s.Repo, "symbolic-ref", "-q", "refs/remotes/origin/HEAD")
		if base == "" {
			var candidates []string
			for _, ref := range []string{"refs/heads/main", "refs/heads/master"} {
				if _, e := resolve(ctx, s.Repo, ref); e == nil {
					candidates = append(candidates, ref)
				}
			}
			if len(candidates) != 1 {
				return s, fmt.Errorf("cannot determine default branch; specify --base REF")
			}
			base = candidates[0]
		}
	}
	s.Base, err = resolve(ctx, s.Repo, base)
	if err != nil {
		return
	}
	if err = os.MkdirAll("/tmp/bosun-repos", 0755); err != nil {
		return
	}
	s.Path, err = os.MkdirTemp("/tmp/bosun-repos", "review.")
	if err != nil {
		return
	}
	defer func() {
		if err != nil {
			os.RemoveAll(s.Path)
		}
	}()
	_, err = git(ctx, s.Repo, "clone", "--quiet", "--mirror", "--no-hardlinks", "--dissociate", "--", s.Repo, filepath.Join(s.Path, ".git"))
	if err != nil {
		return
	}
	_, err = git(ctx, s.Path, "config", "core.bare", "false")
	if err != nil {
		return
	}
	_, err = git(ctx, s.Path, "update-ref", "--no-deref", "HEAD", s.Head)
	if err != nil {
		return
	}
	if s.Committed {
		_, err = git(ctx, s.Path, "reset", "--hard", s.Head)
		if err != nil {
			return
		}
		// Match the local snapshot's top-level .env exclusion.
		err = os.RemoveAll(filepath.Join(s.Path, ".env"))
		if err != nil {
			return
		}
	} else {
		index, e := git(ctx, s.Repo, "rev-parse", "--path-format=absolute", "--git-path", "index")
		if e != nil {
			return s, e
		}
		if _, e = os.Stat(index); e == nil {
			if err = copyFile(index, filepath.Join(s.Path, ".git", "index"), 0600); err != nil {
				return
			}
		}
		common, e := git(ctx, s.Repo, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if e != nil {
			return s, e
		}
		for _, dir := range []string{filepath.Dir(index), common} {
			matches, _ := filepath.Glob(filepath.Join(dir, "sharedindex.*"))
			for _, f := range matches {
				if err = copyFile(f, filepath.Join(s.Path, ".git", filepath.Base(f)), 0600); err != nil {
					return
				}
			}
		}
		// Enumerate from git rather than walking the filesystem. A walk copies
		// every regular file under the worktree, which means Git-ignored
		// secrets -- .env.local, nested .env files, .npmrc, credential caches --
		// are handed to the remote AI agent even though they are neither
		// tracked nor part of the changes under review. ls-files with
		// --exclude-standard yields exactly the tracked files plus the
		// untracked-but-not-ignored ones, which is what a reviewer needs to
		// see.
		var listed string
		listed, err = git(ctx, s.Repo, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
		if err != nil {
			return
		}
		for _, rel := range strings.Split(listed, "\x00") {
			if rel == "" || rel == ".env" {
				continue
			}
			source := filepath.Join(s.Repo, rel)
			info, e := os.Lstat(source)
			if e != nil {
				// Listed in the index but deleted from the worktree: the
				// deletion is itself part of the uncommitted change.
				if os.IsNotExist(e) {
					continue
				}
				return s, e
			}
			dest := filepath.Join(s.Path, rel)
			if err = os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
				return
			}
			if info.Mode()&os.ModeSymlink != 0 {
				target, e := os.Readlink(source)
				if e != nil {
					return s, e
				}
				if err = os.Symlink(target, dest); err != nil {
					return
				}
				continue
			}
			if !info.Mode().IsRegular() {
				continue
			}
			if err = copyFile(source, dest, info.Mode().Perm()); err != nil {
				return
			}
		}
		after, e := git(ctx, s.Repo, "status", "--porcelain=v1", "--untracked-files=all")
		if e != nil {
			return s, e
		}
		head, e := resolve(ctx, s.Repo, "HEAD")
		if e != nil {
			return s, e
		}
		indexAfter, _ := os.ReadFile(indexPath)
		if before != after || head != sourceHead || beforeHash != sha256.Sum256(indexAfter) {
			return s, fmt.Errorf("checkout changed while snapshotting; retry review")
		}
	}
	merge, e := git(ctx, s.Path, "merge-base", s.Base, s.Head)
	if e != nil {
		return s, e
	}
	changed, e := git(ctx, s.Path, "diff", "--name-only", merge)
	if e != nil {
		return s, e
	}
	if changed != "" {
		s.Changes = len(strings.Split(changed, "\n"))
	}
	untracked, _ := git(ctx, s.Path, "ls-files", "--others", "--exclude-standard")
	if untracked != "" {
		s.Changes += len(strings.Split(untracked, "\n"))
	}
	err = filepath.WalkDir(s.Path, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		i, e := d.Info()
		if e != nil {
			return e
		}
		mode := i.Mode().Perm() | 0060
		if d.IsDir() {
			mode |= 0010
		}
		return os.Chmod(p, mode)
	})
	return
}
func copyFile(src, dest string, mode os.FileMode) error {
	in, e := os.Open(src)
	if e != nil {
		return e
	}
	defer in.Close()
	out, e := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if e != nil {
		return e
	}
	before, statErr := in.Stat()
	if statErr != nil {
		out.Close()
		return statErr
	}
	_, e = io.Copy(out, in)
	after, statErr := in.Stat()
	closeErr := out.Close()
	if statErr != nil {
		return statErr
	}
	if before.Size() != after.Size() || before.ModTime() != after.ModTime() {
		return fmt.Errorf("source changed while snapshotting: %s; retry review", src)
	}
	if e != nil {
		return e
	}
	return closeErr
}
