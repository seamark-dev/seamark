package bench

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const publicBenchmarkCacheEnv = "SEAMARK_BENCH_CACHE_DIR"

type publicRepositorySpec struct {
	InstanceID string
	CacheKey   string
	URL        string
	Commit     string
	ModuleDir  string
	VendorSHA  string
}

// PrepareInstance downloads and verifies the immutable source and offline
// dependencies required by a public-repository benchmark. Preparation is
// deliberately separate from Run: agent trials remain network-isolated and a
// paid run cannot unexpectedly turn into a repository download.
func PrepareInstance(ctx context.Context, instance Instance) (string, error) {
	if instance.Prepare == nil {
		return "", fmt.Errorf("benchmark instance %q requires no external preparation", instance.ID)
	}

	return instance.Prepare(ctx)
}

func (s publicRepositorySpec) prepare(ctx context.Context) (string, error) {
	root, err := publicBenchmarkCacheRoot()
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("create public benchmark cache: %w", err)
	}

	target := filepath.Join(root, s.CacheKey)

	if _, err := os.Lstat(target); err == nil {
		if err := s.validateCache(target); err != nil {
			return "", fmt.Errorf("public benchmark cache %s is invalid: %w; remove that cache directory and prepare again", target, err)
		}

		return target, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("inspect public benchmark cache %s: %w", target, err)
	}

	tmp, err := os.MkdirTemp(root, ".prepare-"+s.CacheKey+"-")
	if err != nil {
		return "", fmt.Errorf("create public benchmark preparation directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	if err := runPreparationCommand(ctx, "", "git", "init", "-q", "-b", "main", tmp); err != nil {
		return "", err
	}

	if err := runPreparationCommand(ctx, tmp, "git", "remote", "add", "origin", s.URL); err != nil {
		return "", err
	}

	if err := runPreparationCommand(ctx, tmp, "git", "fetch", "--depth=1", "origin", s.Commit); err != nil {
		return "", err
	}

	if err := runPreparationCommand(ctx, tmp, "git", "checkout", "-q", "--detach", "FETCH_HEAD"); err != nil {
		return "", err
	}

	prepareCache := filepath.Join(tmp, ".seamark-bench-prepare")
	if err := os.MkdirAll(prepareCache, 0o755); err != nil {
		return "", err
	}

	goEnv := append(
		preparationEnvironment(),
		"GOMODCACHE="+filepath.Join(prepareCache, "go-mod"),
		"GOPATH="+filepath.Join(prepareCache, "go-path"),
		"GOCACHE="+filepath.Join(prepareCache, "go-build"),
		"GOENV=off",
		"GOFLAGS=-modcacherw",
		"GOTOOLCHAIN=local",
		"GOWORK=off",
	)

	if err := runPreparationCommandEnv(ctx, filepath.Join(tmp, filepath.FromSlash(s.ModuleDir)), goEnv,
		"go", "mod", "vendor"); err != nil {
		return "", err
	}

	if err := os.RemoveAll(prepareCache); err != nil {
		return "", fmt.Errorf("remove public benchmark preparation cache: %w", err)
	}

	if err := s.validateCache(tmp); err != nil {
		return "", fmt.Errorf("validate prepared public repository: %w", err)
	}

	if err := os.Rename(tmp, target); err != nil {
		if validateErr := s.validateCache(target); validateErr == nil {
			return target, nil
		}

		return "", fmt.Errorf("publish public benchmark cache: %w", err)
	}

	return target, nil
}

func (s publicRepositorySpec) generate(dir string) error {
	cache, err := s.cacheDir()
	if err != nil {
		return err
	}

	if err := s.validateCache(cache); err != nil {
		return fmt.Errorf("public benchmark source is not prepared: %w; run `make lessons-bench-prepare BENCH_INSTANCE=%s`",
			err, s.InstanceID)
	}

	cloneCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if err := runPreparationCommand(cloneCtx, "", "git", "-c", "advice.detachedHead=false",
		"clone", "-q", "--no-local", cache, dir); err != nil {
		return fmt.Errorf("clone prepared public benchmark source: %w", err)
	}

	if err := runPreparationCommand(cloneCtx, dir, "git", "remote", "set-url", "origin", s.URL); err != nil {
		return err
	}

	srcVendor := filepath.Join(cache, filepath.FromSlash(s.ModuleDir), "vendor")
	dstVendor := filepath.Join(dir, filepath.FromSlash(s.ModuleDir), "vendor")

	if err := copyTree(srcVendor, dstVendor); err != nil {
		return fmt.Errorf("copy public benchmark vendor tree: %w", err)
	}

	if err := appendGitExclude(dir, filepath.ToSlash(filepath.Join(s.ModuleDir, "vendor"))); err != nil {
		return err
	}

	if head := fixtureHead(dir); head != s.Commit {
		return fmt.Errorf("public benchmark checkout HEAD %q, want %q", head, s.Commit)
	}

	if out, err := runPreparationCommandOutput(cloneCtx, dir, "git", "status", "--porcelain"); err != nil {
		return fmt.Errorf("inspect public benchmark checkout: %w", err)
	} else if len(out) != 0 {
		return fmt.Errorf("public benchmark checkout is dirty: %s", out)
	}

	return nil
}

func (s publicRepositorySpec) cacheDir() (string, error) {
	root, err := publicBenchmarkCacheRoot()
	if err != nil {
		return "", err
	}

	return filepath.Join(root, s.CacheKey), nil
}

func publicBenchmarkCacheRoot() (string, error) {
	if configured := strings.TrimSpace(os.Getenv(publicBenchmarkCacheEnv)); configured != "" {
		abs, err := filepath.Abs(configured)
		if err != nil {
			return "", fmt.Errorf("resolve %s: %w", publicBenchmarkCacheEnv, err)
		}

		return abs, nil
	}

	cache, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locate user cache: %w", err)
	}

	return filepath.Join(cache, "seamark", "bench"), nil
}

func (s publicRepositorySpec) validateCache(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}

	if !info.IsDir() {
		return fmt.Errorf("cache path is not a directory")
	}

	if head := fixtureHead(dir); head != s.Commit {
		return fmt.Errorf("cached HEAD %q, want %q", head, s.Commit)
	}

	vendor := filepath.Join(dir, filepath.FromSlash(s.ModuleDir), "vendor")
	digest, err := treeSHA256(vendor)
	if err != nil {
		return fmt.Errorf("hash cached vendor tree: %w", err)
	}

	if digest != s.VendorSHA {
		return fmt.Errorf("cached vendor digest %q, want %q", digest, s.VendorSHA)
	}

	return nil
}

func runPreparationCommand(ctx context.Context, dir, name string, args ...string) error {
	_, err := runPreparationCommandOutput(ctx, dir, name, args...)

	return err
}

func runPreparationCommandOutput(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	return runPreparationCommandOutputEnv(ctx, dir, preparationEnvironment(), name, args...)
}

func preparationEnvironment() []string {
	return append(
		sanitizedEnvironment(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
	)
}

func runPreparationCommandEnv(ctx context.Context, dir string, env []string, name string, args ...string) error {
	_, err := runPreparationCommandOutputEnv(ctx, dir, env, name, args...)

	return err
}

func runPreparationCommandOutputEnv(ctx context.Context, dir string, env []string,
	name string, args ...string,
) ([]byte, error) {
	commandCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(commandCtx, name, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.WaitDelay = processWaitDelay

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		if commandCtx.Err() != nil {
			return nil, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), commandCtx.Err())
		}

		return nil, fmt.Errorf("%s %s: %w\n%s%s", name, strings.Join(args, " "), err,
			stdout.Bytes(), stderr.Bytes())
	}

	return stdout.Bytes(), nil
}

func treeSHA256(root string) (string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if entry.Type().IsRegular() {
			files = append(files, path)
		}

		return nil
	})
	if err != nil {
		return "", err
	}

	if len(files) == 0 {
		return "", fmt.Errorf("tree contains no regular files")
	}

	sort.Strings(files)

	tree := sha256.New()
	for _, path := range files {
		file, err := os.Open(path)
		if err != nil {
			return "", err
		}

		fileHash := sha256.New()
		_, copyErr := io.Copy(fileHash, file)
		closeErr := file.Close()

		if copyErr != nil {
			return "", copyErr
		}

		if closeErr != nil {
			return "", closeErr
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return "", err
		}
		_, _ = fmt.Fprintf(tree, "%s  ./%s\n", hex.EncodeToString(fileHash.Sum(nil)), filepath.ToSlash(rel))
	}

	return hex.EncodeToString(tree.Sum(nil)), nil
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}

		target := filepath.Join(dst, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}

		if !entry.Type().IsRegular() {
			return fmt.Errorf("unsupported file type in public benchmark cache: %s", path)
		}

		in, err := os.Open(path)
		if err != nil {
			return err
		}

		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			_ = in.Close()
			return err
		}

		_, copyErr := io.Copy(out, in)
		inCloseErr := in.Close()
		closeErr := out.Close()

		if copyErr != nil {
			return copyErr
		}

		if inCloseErr != nil {
			return inCloseErr
		}

		return closeErr
	})
}

func appendGitExclude(dir, rel string) error {
	path := filepath.Join(dir, ".git", "info", "exclude")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open public benchmark git exclude: %w", err)
	}
	defer func() { _ = file.Close() }()

	if _, err := fmt.Fprintf(file, "\n# benchmark offline dependencies\n/%s/\n", strings.Trim(rel, "/")); err != nil {
		return fmt.Errorf("write public benchmark git exclude: %w", err)
	}

	return nil
}
