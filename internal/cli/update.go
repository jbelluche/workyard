package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/jackbelluche/workyard/internal/output"
	"github.com/jackbelluche/workyard/internal/remote"
	"github.com/spf13/cobra"
)

const (
	defaultUpdateRepo    = "jbelluche/workyard"
	defaultUpdateVersion = "latest"
	defaultUpdateMethod  = "auto"
)

var (
	updateRepoPattern    = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	updateVersionPattern = regexp.MustCompile(`^(latest|v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?)$`)
	updateRefPattern     = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
)

type localUpdateOptions struct {
	Version      string
	Repo         string
	Method       string
	Ref          string
	InstallDir   string
	InstallerURL string
	ShellUpdate  bool
	DryRun       bool
}

type localUpdatePlan struct {
	CurrentVersion   string   `json:"currentVersion"`
	RequestedVersion string   `json:"requestedVersion"`
	InstalledVersion string   `json:"installedVersion,omitempty"`
	Repo             string   `json:"repo"`
	Method           string   `json:"method"`
	Ref              string   `json:"ref,omitempty"`
	InstallDir       string   `json:"installDir"`
	InstallDirSource string   `json:"installDirSource"`
	InstallerURL     string   `json:"installerUrl"`
	InstallerArgs    []string `json:"installerArgs"`
	ShellUpdate      bool     `json:"shellUpdate"`
	DryRun           bool     `json:"dryRun"`
}

type localUpdateResult struct {
	OK               bool   `json:"ok"`
	CurrentVersion   string `json:"currentVersion"`
	RequestedVersion string `json:"requestedVersion"`
	InstalledVersion string `json:"installedVersion,omitempty"`
	Repo             string `json:"repo"`
	Method           string `json:"method"`
	Ref              string `json:"ref,omitempty"`
	InstallDir       string `json:"installDir"`
	InstallerURL     string `json:"installerUrl"`
	DryRun           bool   `json:"dryRun,omitempty"`
	Stdout           string `json:"stdout,omitempty"`
	Stderr           string `json:"stderr,omitempty"`
}

func updateCommand(opts *options) *cobra.Command {
	updateOpts := localUpdateOptions{
		Version: defaultUpdateVersion,
		Repo:    defaultUpdateRepo,
		Method:  defaultUpdateMethod,
	}
	cmd := &cobra.Command{
		Use:     "update",
		Aliases: []string{"upgrade"},
		Short:   "Update the local Workyard binary",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			plan, err := buildLocalUpdatePlan(updateOpts)
			if err != nil {
				return err
			}
			if updateOpts.DryRun {
				plan.DryRun = true
				if opts.json {
					return output.WriteJSON(cmd.OutOrStdout(), struct {
						OK bool `json:"ok"`
						localUpdatePlan
					}{OK: true, localUpdatePlan: plan})
				}
				printLocalUpdatePlan(cmd.OutOrStdout(), plan)
				return nil
			}
			if !opts.quiet && !opts.json {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "updating workyard %s -> %s\n", plan.CurrentVersion, plan.RequestedVersion)
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "install dir: %s\n", plan.InstallDir)
			}
			stdout, stderr, err := runLocalUpdate(cmd.Context(), plan, cmd.OutOrStdout(), cmd.ErrOrStderr(), opts.quiet || opts.json)
			if err != nil {
				return output.NewError("UPDATE_FAILED", err.Error(), "Rerun with --verbose or run the install script manually from the README")
			}
			installedVersion, err := installedLocalWorkyardVersion(cmd.Context(), plan.InstallDir)
			if err != nil {
				return output.NewError("UPDATE_VERIFY_FAILED", err.Error(), "Check that "+filepath.Join(plan.InstallDir, "workyard")+" is executable")
			}
			if opts.json {
				return output.WriteJSON(cmd.OutOrStdout(), localUpdateResult{
					OK:               true,
					CurrentVersion:   plan.CurrentVersion,
					RequestedVersion: plan.RequestedVersion,
					InstalledVersion: installedVersion,
					Repo:             plan.Repo,
					Method:           plan.Method,
					Ref:              plan.Ref,
					InstallDir:       plan.InstallDir,
					InstallerURL:     plan.InstallerURL,
					Stdout:           strings.TrimSpace(stdout),
					Stderr:           strings.TrimSpace(stderr),
				})
			}
			if !opts.quiet {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "workyard is now %s at %s\n", installedVersion, filepath.Join(plan.InstallDir, "workyard"))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&updateOpts.Version, "version", defaultUpdateVersion, "release version/tag to install")
	cmd.Flags().StringVar(&updateOpts.Repo, "repo", defaultUpdateRepo, "GitHub repository owner/name")
	cmd.Flags().StringVar(&updateOpts.Method, "method", defaultUpdateMethod, "install method: auto, release, or source")
	cmd.Flags().StringVar(&updateOpts.Ref, "ref", "", "source ref for --method source")
	cmd.Flags().StringVar(&updateOpts.InstallDir, "install-dir", "", "directory to install workyard into (defaults to the current binary directory)")
	cmd.Flags().BoolVar(&updateOpts.ShellUpdate, "shell-update", false, "allow the installer to add the install directory to your shell profile")
	cmd.Flags().BoolVar(&updateOpts.DryRun, "dry-run", false, "show the update plan without downloading or installing")
	cmd.Flags().StringVar(&updateOpts.InstallerURL, "installer-url", "", "installer script URL")
	_ = cmd.Flags().MarkHidden("installer-url")
	return cmd
}

func buildLocalUpdatePlan(opts localUpdateOptions) (localUpdatePlan, error) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return localUpdatePlan{}, output.NewError("UPDATE_PLATFORM_UNSUPPORTED", "workyard update supports macOS and Linux", "Use the install script manually on a supported platform")
	}
	opts.Version = strings.TrimSpace(opts.Version)
	opts.Repo = strings.TrimSpace(opts.Repo)
	opts.Method = strings.TrimSpace(opts.Method)
	opts.Ref = strings.TrimSpace(opts.Ref)
	if !updateVersionPattern.MatchString(opts.Version) {
		return localUpdatePlan{}, output.NewError("UPDATE_VERSION_INVALID", "version must be latest or look like v0.1.0", "")
	}
	if !updateRepoPattern.MatchString(opts.Repo) {
		return localUpdatePlan{}, output.NewError("UPDATE_REPO_INVALID", "repo must look like owner/name", "")
	}
	switch opts.Method {
	case "auto", "release", "source":
	default:
		return localUpdatePlan{}, output.NewError("UPDATE_METHOD_INVALID", "method must be auto, release, or source", "")
	}
	if opts.Ref != "" {
		if !updateRefPattern.MatchString(opts.Ref) || strings.Contains(opts.Ref, "..") || strings.Contains(opts.Ref, "//") || strings.HasPrefix(opts.Ref, "-") || strings.HasPrefix(opts.Ref, "/") || strings.HasSuffix(opts.Ref, "/") {
			return localUpdatePlan{}, output.NewError("UPDATE_REF_INVALID", "ref contains unsupported characters", "Use a branch, tag, or commit-ish without whitespace or shell metacharacters")
		}
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return localUpdatePlan{}, output.NewError("UPDATE_HOME_INVALID", "could not determine HOME", "")
	}
	executable, _ := os.Executable()
	installDir, installDirSource, err := resolveUpdateInstallDir(opts.InstallDir, executable, home)
	if err != nil {
		return localUpdatePlan{}, err
	}
	installerURL := strings.TrimSpace(opts.InstallerURL)
	if installerURL == "" {
		installerURL = "https://raw.githubusercontent.com/" + opts.Repo + "/main/scripts/install.sh"
	}
	if err := validateUpdateInstallerURL(installerURL); err != nil {
		return localUpdatePlan{}, err
	}
	args := []string{
		"--install-dir", installDir,
		"--version", opts.Version,
		"--repo", opts.Repo,
		"--method", opts.Method,
	}
	if opts.Ref != "" {
		args = append(args, "--ref", opts.Ref)
	}
	if !opts.ShellUpdate {
		args = append(args, "--no-shell-update")
	}
	return localUpdatePlan{
		CurrentVersion:   Version,
		RequestedVersion: opts.Version,
		Repo:             opts.Repo,
		Method:           opts.Method,
		Ref:              opts.Ref,
		InstallDir:       installDir,
		InstallDirSource: installDirSource,
		InstallerURL:     installerURL,
		InstallerArgs:    args,
		ShellUpdate:      opts.ShellUpdate,
	}, nil
}

func resolveUpdateInstallDir(explicit, executable, home string) (string, string, error) {
	home = filepath.Clean(home)
	if explicit != "" {
		dir := filepath.Clean(explicit)
		if !filepath.IsAbs(dir) {
			return "", "", output.NewError("UPDATE_INSTALL_DIR_INVALID", "install directory must be absolute: "+explicit, "")
		}
		if !isPathInside(home, dir) {
			return "", "", output.NewError("UPDATE_INSTALL_DIR_INVALID", "install directory must be under HOME: "+dir, "Choose a directory such as "+filepath.Join(home, ".local", "bin"))
		}
		return dir, "flag", nil
	}
	if executable != "" {
		executable = filepath.Clean(executable)
		dir := filepath.Dir(executable)
		if filepath.Base(executable) == "workyard" && isPathInside(home, dir) && !hasPathComponent(executable, "go-build") {
			return dir, "current-binary", nil
		}
	}
	return filepath.Join(home, ".local", "bin"), "default", nil
}

func isPathInside(root, value string) bool {
	rel, err := filepath.Rel(root, value)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func hasPathComponent(value, component string) bool {
	value = filepath.Clean(value)
	for {
		if filepath.Base(value) == component {
			return true
		}
		parent := filepath.Dir(value)
		if parent == value {
			return false
		}
		value = parent
	}
}

func validateUpdateInstallerURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return output.NewError("UPDATE_INSTALLER_URL_INVALID", "installer URL is invalid", "")
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return output.NewError("UPDATE_INSTALLER_URL_INVALID", "installer URL must use http or https", "")
	}
	return nil
}

func printLocalUpdatePlan(w io.Writer, plan localUpdatePlan) {
	_, _ = fmt.Fprintf(w, "would update workyard %s -> %s\n", plan.CurrentVersion, plan.RequestedVersion)
	_, _ = fmt.Fprintf(w, "installer: %s\n", plan.InstallerURL)
	_, _ = fmt.Fprintf(w, "install dir: %s (%s)\n", plan.InstallDir, plan.InstallDirSource)
	_, _ = fmt.Fprintf(w, "command: sh <downloaded-installer> %s\n", shellQuoteArgs(plan.InstallerArgs))
}

func shellQuoteArgs(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, remote.ShellQuote(arg))
	}
	return strings.Join(quoted, " ")
}

func runLocalUpdate(ctx context.Context, plan localUpdatePlan, stdout, stderr io.Writer, capture bool) (string, string, error) {
	installerPath, cleanup, err := downloadUpdateInstaller(ctx, plan.InstallerURL)
	if err != nil {
		return "", "", err
	}
	defer cleanup()
	argv := append([]string{installerPath}, plan.InstallerArgs...)
	cmd := exec.CommandContext(ctx, "sh", argv...)
	cmd.Env = os.Environ()
	var outBuf bytes.Buffer
	var errBuf bytes.Buffer
	if capture {
		cmd.Stdout = &outBuf
		cmd.Stderr = &errBuf
	} else {
		cmd.Stdout = stdout
		cmd.Stderr = stderr
	}
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(errBuf.String())
		if detail != "" {
			return outBuf.String(), errBuf.String(), fmt.Errorf("installer failed: %w: %s", err, truncateForDisplay(detail, 2048))
		}
		return outBuf.String(), errBuf.String(), fmt.Errorf("installer failed: %w", err)
	}
	return outBuf.String(), errBuf.String(), nil
}

func downloadUpdateInstaller(ctx context.Context, installerURL string) (string, func(), error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, installerURL, nil)
	if err != nil {
		return "", nil, fmt.Errorf("build installer request: %w", err)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("download installer: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return "", nil, fmt.Errorf("download installer: HTTP %s", res.Status)
	}
	file, err := os.CreateTemp("", "workyard-install-*.sh")
	if err != nil {
		return "", nil, fmt.Errorf("create installer temp file: %w", err)
	}
	cleanup := func() { _ = os.Remove(file.Name()) }
	const maxInstallerBytes = 2 << 20
	n, copyErr := io.Copy(file, io.LimitReader(res.Body, maxInstallerBytes+1))
	closeErr := file.Close()
	if copyErr != nil {
		cleanup()
		return "", nil, fmt.Errorf("write installer temp file: %w", copyErr)
	}
	if closeErr != nil {
		cleanup()
		return "", nil, fmt.Errorf("close installer temp file: %w", closeErr)
	}
	if n > maxInstallerBytes {
		cleanup()
		return "", nil, fmt.Errorf("installer script is larger than %d bytes", maxInstallerBytes)
	}
	return file.Name(), cleanup, nil
}

func installedLocalWorkyardVersion(ctx context.Context, installDir string) (string, error) {
	binary := filepath.Join(installDir, "workyard")
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "version", "--json")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail != "" {
			return "", fmt.Errorf("%s version --json failed: %w: %s", binary, err, detail)
		}
		return "", fmt.Errorf("%s version --json failed: %w", binary, err)
	}
	var version struct {
		OK      bool   `json:"ok"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(out, &version); err != nil {
		return "", fmt.Errorf("decode installed version: %w", err)
	}
	if !version.OK || strings.TrimSpace(version.Version) == "" {
		return "", fmt.Errorf("installed version response was not ok")
	}
	return strings.TrimSpace(version.Version), nil
}
