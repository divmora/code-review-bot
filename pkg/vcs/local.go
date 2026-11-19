package vcs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/divmora/code-review-bot/pkg/diff"
)

// LocalClient reviews a local git checkout. It never publishes; posting
// methods return an error.
type LocalClient struct {
	Dir string
	// Base is the ref to diff against. Empty reviews uncommitted changes
	// (working tree and index vs HEAD).
	Base string
}

func (l *LocalClient) Platform() string { return Local }

func (l *LocalClient) git(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = l.Dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// emptyTree is git's well-known empty tree object, used as the base in a
// repository with no commits yet.
const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

func (l *LocalClient) GetPR(ctx context.Context, ref Ref) (*PR, error) {
	if _, err := l.git(ctx, "rev-parse", "--git-dir"); err != nil {
		return nil, fmt.Errorf("%s is not a git repository: %w", l.Dir, err)
	}
	head, err := l.git(ctx, "rev-parse", "--verify", "-q", "HEAD")
	if err != nil {
		if l.Base != "" {
			return nil, fmt.Errorf("repository has no commits, so --base %s cannot be used", l.Base)
		}
		return &PR{Ref: ref, Title: "Initial changes (no commits yet)", BaseSHA: emptyTree}, nil
	}
	branch, _ := l.git(ctx, "rev-parse", "--abbrev-ref", "HEAD")
	pr := &PR{Ref: ref, HeadSHA: strings.TrimSpace(string(head)), HeadBranch: strings.TrimSpace(string(branch))}
	if l.Base != "" {
		mb, err := l.git(ctx, "merge-base", l.Base, "HEAD")
		if err != nil {
			return nil, err
		}
		pr.BaseSHA = strings.TrimSpace(string(mb))
		pr.BaseBranch = l.Base
		log, _ := l.git(ctx, "log", "--format=%s%n%n%b", l.Base+"..HEAD")
		pr.Title = "Local changes on " + pr.HeadBranch
		pr.Body = strings.TrimSpace(string(log))
	} else {
		pr.BaseSHA = pr.HeadSHA
		pr.Title = "Uncommitted changes on " + pr.HeadBranch
	}
	return pr, nil
}

func (l *LocalClient) Files(ctx context.Context, pr *PR) ([]diff.File, error) {
	args := []string{"diff", "--no-color", "--no-ext-diff", "--unified=3", "-M"}
	switch {
	case l.Base != "":
		args = append(args, pr.BaseSHA)
	case pr.HeadSHA == "":
		args = append(args, "--cached", emptyTree) // staged files in a new repo
	default:
		args = append(args, "HEAD")
	}
	out, err := l.git(ctx, args...)
	if err != nil {
		return nil, err
	}
	files, err := diff.ParseMulti(string(out))
	if err != nil {
		return nil, err
	}
	if l.Base == "" {
		// Include untracked files as additions.
		untracked, _ := l.git(ctx, "ls-files", "--others", "--exclude-standard")
		for _, p := range strings.Fields(string(untracked)) {
			data, err := os.ReadFile(filepath.Join(l.Dir, p))
			if err != nil || bytes.IndexByte(data, 0) >= 0 {
				continue
			}
			lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
			h := diff.Hunk{OldStart: 0, NewStart: 1, NewLines: len(lines)}
			for i, t := range lines {
				h.Lines = append(h.Lines, diff.Line{Kind: diff.Add, NewNo: i + 1, Text: t})
			}
			files = append(files, diff.File{Path: p, OldPath: p, Status: diff.Added, Hunks: []diff.Hunk{h}})
		}
	}
	return files, nil
}

// FileContent reads the working tree (the reviewed state).
func (l *LocalClient) FileContent(ctx context.Context, pr *PR, path, ref string) ([]byte, error) {
	clean := filepath.Clean(path)
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
		return nil, fmt.Errorf("path %q escapes the repository", path)
	}
	data, err := os.ReadFile(filepath.Join(l.Dir, clean))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return data, err
}

func (l *LocalClient) ChangedPaths(ctx context.Context, pr *PR, from, to string) ([]string, error) {
	out, err := l.git(ctx, "diff", "--name-only", from, to)
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(out)), nil
}

func (l *LocalClient) BotState(context.Context, *PR) (*BotState, error) {
	return &BotState{Fingerprints: map[string]bool{}}, nil
}

var errLocalPost = errors.New("local reviews cannot be posted; use --format to print or save them")

func (l *LocalClient) PostInline(context.Context, *PR, string, []Comment) ([]Comment, error) {
	return nil, errLocalPost
}
func (l *LocalClient) UpsertSummary(context.Context, *PR, *BotState, string) error {
	return errLocalPost
}
func (l *LocalClient) PostComment(context.Context, *PR, string) error   { return errLocalPost }
func (l *LocalClient) React(context.Context, *PR, string, string) error { return nil }

func jsonUnmarshal(raw []byte, v any) error {
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// SetScoreLabels is not available for local reviews.
func (l *LocalClient) SetScoreLabels(context.Context, *PR, []ScoreLabel) error { return errLocalPost }

// UpdatePR is not available for local reviews.
func (l *LocalClient) UpdatePR(context.Context, *PR, *string, *string) error { return errLocalPost }

// SetCommitStatus is not available for local reviews.
func (l *LocalClient) SetCommitStatus(context.Context, *PR, string, CommitStatus) error {
	return errLocalPost
}
