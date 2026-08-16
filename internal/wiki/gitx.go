package wiki

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// Git operations shell out to the git CLI (as the Rust original does for
// log), keeping the binary dependency-free of libgit2 bindings.

// GitInit initialises a new git repository at path.
func GitInit(path string) error {
	cmd := exec.Command("git", "init")
	cmd.Dir = path
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to init git repo at %s: %s", path, strings.TrimSpace(string(out)))
	}
	return nil
}

// GitCommit stages all files and commits; returns "" when nothing changed.
func GitCommit(repoRoot, message string) (string, error) {
	if err := gitRun(repoRoot, "add", "-A"); err != nil {
		return "", err
	}
	status, err := gitOut(repoRoot, "status", "--porcelain")
	if err != nil {
		return "", err
	}
	if status == "" {
		return "", nil // nothing staged — nothing to commit
	}
	out, err := gitOut(repoRoot, "commit", "-m", message)
	if err != nil {
		// "nothing to commit" races are benign
		if strings.Contains(out, "nothing to commit") {
			return "", nil
		}
		return "", err
	}
	// commit prints "[branch abbrev-hash] msg" — return the full hash
	return GitCurrentHead(repoRoot), nil
}

// GitCommitPaths stages specific paths and commits; "" when nothing changed.
func GitCommitPaths(repoRoot string, paths []string, message string) (string, error) {
	args := []string{"add", "--"}
	for _, p := range paths {
		rel, err := filepath.Rel(repoRoot, p)
		if err != nil {
			rel = p
		}
		args = append(args, rel)
	}
	if err := gitRun(repoRoot, args...); err != nil {
		return "", err
	}
	if _, err := gitOut(repoRoot, "diff", "--cached", "--quiet"); err == nil {
		return "", nil
	}
	out, err := gitOut(repoRoot, "commit", "-m", message)
	if err != nil {
		if strings.Contains(out, "nothing to commit") {
			return "", nil
		}
		return "", err
	}
	return GitCurrentHead(repoRoot), nil
}

// GitCurrentHead returns the HEAD commit hash, or "" with no commits.
func GitCurrentHead(repoRoot string) string {
	out, err := gitOut(repoRoot, "rev-parse", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// DeltaKind classifies a git change.
type DeltaKind string

// Git delta statuses.
const (
	DeltaAdded    DeltaKind = "A"
	DeltaModified DeltaKind = "M"
	DeltaDeleted  DeltaKind = "D"
	DeltaRenamed  DeltaKind = "R"
	DeltaCopied   DeltaKind = "C"
	DeltaOther    DeltaKind = "?"
)

// ChangedFile is a file that changed between git states.
type ChangedFile struct {
	Path   string // repo-relative, POSIX separators
	Status DeltaKind
}

// GitChangedWikiFiles detects changed .md files under wikiRoot in the
// working tree (including untracked) vs HEAD.
func GitChangedWikiFiles(repoRoot, wikiRoot string) ([]ChangedFile, error) {
	out, err := gitOut(repoRoot, "-c", "core.quotePath=false", "status", "--porcelain", "--untracked-files=all", "--", relUnder(repoRoot, wikiRoot))
	if err != nil {
		return nil, err
	}
	return parsePorcelain(out, relUnder(repoRoot, wikiRoot)), nil
}

// GitChangedSinceCommit detects changed .md files between a past commit and HEAD.
func GitChangedSinceCommit(repoRoot, wikiRoot, fromCommit string) ([]ChangedFile, error) {
	out, err := gitOut(repoRoot, "-c", "core.quotePath=false", "diff", "--name-status", fromCommit, "HEAD", "--", relUnder(repoRoot, wikiRoot))
	if err != nil {
		return nil, err
	}
	prefix := relUnder(repoRoot, wikiRoot)
	appendMd := func(rawPath string, status DeltaKind, changes *[]ChangedFile) {
		path := filepath.ToSlash(rawPath)
		if strings.HasPrefix(path, prefix) && strings.HasSuffix(path, ".md") {
			*changes = append(*changes, ChangedFile{Path: path, Status: status})
		}
	}
	var changes []ChangedFile
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) < 2 {
			continue
		}
		status := normalizeStatus(fields[0])
		if len(fields) == 3 { // "R100\told\tnew" — old dies, new appears
			appendMd(fields[1], DeltaDeleted, &changes)
			appendMd(fields[2], status, &changes)
			continue
		}
		appendMd(fields[1], status, &changes)
	}
	return changes, nil
}

// CollectChangedFiles merges working-tree vs HEAD and lastIndexed vs HEAD
// diffs; working-tree changes win on duplicates.
func CollectChangedFiles(repoRoot, wikiRoot string, lastIndexed string) map[string]DeltaKind {
	changes := map[string]DeltaKind{}
	if lastIndexed != "" {
		if files, err := GitChangedSinceCommit(repoRoot, wikiRoot, lastIndexed); err == nil {
			for _, f := range files {
				changes[f.Path] = f.Status
			}
		}
	}
	if files, err := GitChangedWikiFiles(repoRoot, wikiRoot); err == nil {
		for _, f := range files {
			changes[f.Path] = f.Status
		}
	}
	return changes
}

func parsePorcelain(out, prefix string) []ChangedFile {
	var changes []ChangedFile
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 4 {
			continue
		}
		status := normalizeStatus(strings.TrimSpace(line[:2]))
		path := filepath.ToSlash(strings.TrimSpace(line[3:]))
		// rename format: "R  old -> new" — record the new path and the
		// old path as deleted
		if idx := strings.Index(path, " -> "); idx >= 0 {
			old := path[:idx]
			path = path[idx+4:]
			if strings.HasPrefix(old, prefix) && strings.HasSuffix(old, ".md") {
				changes = append(changes, ChangedFile{Path: old, Status: DeltaDeleted})
			}
		}
		if strings.HasPrefix(path, prefix) && strings.HasSuffix(path, ".md") {
			changes = append(changes, ChangedFile{Path: path, Status: status})
		}
	}
	return changes
}

func normalizeStatus(s string) DeltaKind {
	if s == "" {
		return DeltaOther
	}
	switch c := s[0]; c {
	case 'A', '?':
		return DeltaAdded
	case 'M', 'T':
		return DeltaModified
	case 'D':
		return DeltaDeleted
	case 'R':
		return DeltaRenamed
	case 'C':
		return DeltaCopied
	default:
		return DeltaOther
	}
}

func relUnder(repoRoot, wikiRoot string) string {
	rel, err := filepath.Rel(repoRoot, wikiRoot)
	if err != nil || strings.HasPrefix(rel, "..") {
		panic(fmt.Sprintf("wiki_root %s is not under repo_root %s; check space configuration", wikiRoot, repoRoot))
	}
	return filepath.ToSlash(rel)
}

// HistoryEntry is one git log entry for a page.
type HistoryEntry struct {
	Hash    string `json:"hash"`
	Date    string `json:"date"`
	Message string `json:"message"`
	Author  string `json:"author"`
}

// GitPageHistory returns the commit history for a repo-relative path.
func GitPageHistory(repoRoot, relPath string, limit int, follow bool) ([]HistoryEntry, error) {
	args := []string{"log", "--format=%H%x00%aI%x00%s%x00%an"}
	if follow {
		args = append(args, "--follow")
	}
	if limit > 0 {
		args = append(args, "-n", fmt.Sprint(limit))
	}
	args = append(args, "--", relPath)
	cmd := exec.Command("git", args...)
	cmd.Dir = repoRoot
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if stderr.Len() == 0 {
			return nil, nil // empty history is not an error
		}
		return nil, fmt.Errorf("git log failed: %s", strings.TrimSpace(stderr.String()))
	}
	var entries []HistoryEntry
	for _, line := range strings.Split(stdout.String(), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\x00", 4)
		if len(parts) == 4 {
			entries = append(entries, HistoryEntry{
				Hash: parts[0], Date: parts[1], Message: parts[2], Author: parts[3],
			})
		}
	}
	return entries, nil
}

// GitRecentCommits returns the newest commits for the whole repo
// (repo-wide changelog for the web home page).
func GitRecentCommits(repoRoot string, limit int) ([]HistoryEntry, error) {
	cmd := exec.Command("git", "log", "--format=%H%x00%aI%x00%s%x00%an", "-n", fmt.Sprint(limit))
	cmd.Dir = repoRoot
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if stderr.Len() == 0 {
			return nil, nil
		}
		return nil, fmt.Errorf("git log failed: %s", strings.TrimSpace(stderr.String()))
	}
	var entries []HistoryEntry
	for _, line := range strings.Split(stdout.String(), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\x00", 4)
		if len(parts) == 4 {
			entries = append(entries, HistoryEntry{Hash: parts[0], Date: parts[1], Message: parts[2], Author: parts[3]})
		}
	}
	return entries, nil
}

func gitRun(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s failed: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return nil
}

func gitOut(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("git %s failed: %s", strings.Join(args, " "), strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func firstLine(s string) string {
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return strings.TrimSpace(s[:idx])
	}
	return strings.TrimSpace(s)
}
