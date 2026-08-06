// smoke_test.go is a standalone, self-contained end-to-end check of the
// full FLEX_QURL pipeline: it builds/locates the three service binaries,
// starts flex-web-api and flex-image-viewer as real subprocesses sharing
// a temp job_store, uploads a bundled sample .cur file to
// http://localhost:8080/convert, polls the JSON response for a job ID,
// and then hits http://localhost:8081/view + /raw to prove the converted
// output is actually retrievable -- including the on-the-fly transcode
// path, by requesting a non-web-safe output format.
//
// This replaces manually clicking through the UI to sanity-check a deploy.
//
// Usage (from repo root, after building flex-convert-cli):
//
//	cd scripts && go run smoke_test.go
//
// Optional flags:
//
//	-to string        target conversion format (default "xbm", forces the
//	                   viewer's transcode-on-view path since xbm isn't
//	                   browser-safe)
//	-cli-path string  override path to flex-convert-cli
//	-timeout duration overall timeout (default 30s)
package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

//go:embed testdata/sample.cur
var sampleCUR []byte

func main() {
	toFormat := flag.String("to", "xbm", "target conversion format")
	cliPathFlag := flag.String("cli-path", "", "override path to flex-convert-cli")
	timeout := flag.Duration("timeout", 30*time.Second, "overall timeout")
	flag.Parse()

	if err := run(*toFormat, *cliPathFlag, *timeout); err != nil {
		fmt.Fprintf(os.Stderr, "\nSMOKE TEST FAILED: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("\nSMOKE TEST PASSED")
}

func run(toFormat, cliPathFlag string, timeout time.Duration) error {
	scriptDir, err := os.Getwd()
	if err != nil {
		return err
	}
	repoRoot := filepath.Dir(scriptDir) // scripts/ -> repo root

	step("Locating / building binaries")
	cliBin, err := resolveCLI(repoRoot, cliPathFlag)
	if err != nil {
		return err
	}
	apiBin, err := buildGoBinary(filepath.Join(repoRoot, "flex-webapi"), "flex-web-api")
	if err != nil {
		return fmt.Errorf("building flex-web-api: %w", err)
	}
	viewerBin, err := buildGoBinary(filepath.Join(repoRoot, "flex-image-viewer"), "flex-image-viewer")
	if err != nil {
		return fmt.Errorf("building flex-image-viewer: %w", err)
	}
	ok("cli=%s\n         api=%s\n         viewer=%s", cliBin, apiBin, viewerBin)

	step("Setting up an isolated workspace (temp job_store shared by both services)")
	workDir, err := os.MkdirTemp("", "flex-smoke-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workDir)

	if err := copyFile(cliBin, filepath.Join(workDir, "flex-convert-cli")); err != nil {
		return fmt.Errorf("staging CLI binary: %w", err)
	}
	if err := os.Chmod(filepath.Join(workDir, "flex-convert-cli"), 0o755); err != nil {
		return err
	}
	if err := copyFile(apiBin, filepath.Join(workDir, "flex-web-api")); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Join(workDir, "flex-web-api"), 0o755); err != nil {
		return err
	}
	if err := copyFile(viewerBin, filepath.Join(workDir, "flex-image-viewer")); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Join(workDir, "flex-image-viewer"), 0o755); err != nil {
		return err
	}
	// The API serves ./static; harmless if empty, but stage it if present.
	staticSrc := filepath.Join(repoRoot, "flex-webapi", "static")
	if _, err := os.Stat(staticSrc); err == nil {
		_ = copyDir(staticSrc, filepath.Join(workDir, "static"))
	}
	ok("workspace: %s", workDir)

	step("Starting flex-web-api (:8080) and flex-image-viewer (:8081)")
	apiCmd := exec.Command("./flex-web-api")
	apiCmd.Dir = workDir
	apiCmd.Env = append(os.Environ(), "QT_QPA_PLATFORM=offscreen")
	apiCmd.Stdout = prefixWriter("  [api]    ")
	apiCmd.Stderr = prefixWriter("  [api]    ")
	if err := apiCmd.Start(); err != nil {
		return fmt.Errorf("starting flex-web-api: %w", err)
	}
	defer func() { _ = apiCmd.Process.Kill() }()

	viewerCmd := exec.Command("./flex-image-viewer")
	viewerCmd.Dir = workDir
	viewerCmd.Env = append(os.Environ(), "QT_QPA_PLATFORM=offscreen")
	viewerCmd.Stdout = prefixWriter("  [viewer] ")
	viewerCmd.Stderr = prefixWriter("  [viewer] ")
	if err := viewerCmd.Start(); err != nil {
		return fmt.Errorf("starting flex-image-viewer: %w", err)
	}
	defer func() { _ = viewerCmd.Process.Kill() }()

	if err := waitForPort("localhost:8080", 5*time.Second); err != nil {
		return fmt.Errorf("flex-web-api never came up: %w", err)
	}
	if err := waitForPort("localhost:8081", 5*time.Second); err != nil {
		return fmt.Errorf("flex-image-viewer never came up: %w", err)
	}
	ok("both services accepting connections")

	client := &http.Client{Timeout: timeout}

	step("Uploading bundled sample.cur to POST /convert?to=%s", toFormat)
	jobID, err := uploadCUR(client, toFormat)
	if err != nil {
		return fmt.Errorf("upload/convert step failed: %w", err)
	}
	ok("job_id=%s", jobID)

	step("Verifying GET /view?job=%s returns the viewer HTML shell", jobID)
	viewBody, status, err := getBody(client, fmt.Sprintf("http://localhost:8081/view?job=%s", jobID))
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("expected 200 from /view, got %d", status)
	}
	if !bytes.Contains(viewBody, []byte("/raw?job="+jobID)) {
		return fmt.Errorf("/view response did not embed the expected /raw?job=%s reference", jobID)
	}
	ok("viewer HTML references the correct job")

	step("Verifying GET /raw?job=%s returns real image bytes (exercises transcode-on-view since '%s' isn't browser-safe)", jobID, toFormat)
	rawBody, status, err := getBody(client, fmt.Sprintf("http://localhost:8081/raw?job=%s", jobID))
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("expected 200 from /raw, got %d: %s", status, rawBody)
	}
	if len(rawBody) < 8 || string(rawBody[1:4]) != "PNG" {
		return fmt.Errorf("expected /raw to serve a transcoded PNG, got %d bytes not starting with the PNG signature", len(rawBody))
	}
	ok("received %d bytes of transcoded PNG output", len(rawBody))

	return nil
}

func uploadCUR(client *http.Client, toFormat string) (string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", "sample.cur")
	if err != nil {
		return "", err
	}
	if _, err := fw.Write(sampleCUR); err != nil {
		return "", err
	}
	if err := w.WriteField("preferences", "{}"); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}

	url := fmt.Sprintf("http://localhost:8080/convert?to=%s", toFormat)
	req, err := http.NewRequest(http.MethodPost, url, &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("convert returned %d: %s", resp.StatusCode, body)
	}

	var parsed struct {
		Status string `json:"status"`
		JobID  string `json:"job_id"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("could not parse JSON response %q: %w", body, err)
	}
	if parsed.Status != "success" || parsed.JobID == "" {
		return "", fmt.Errorf("unexpected response: %s", body)
	}
	return parsed.JobID, nil
}

func getBody(client *http.Client, url string) ([]byte, int, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return body, resp.StatusCode, err
}

// resolveCLI finds a built flex-convert-cli. It does NOT build it (that
// needs the Qt5/CMake toolchain) -- it points you at the build steps if
// it's missing, same as flex-cli/README.md.
func resolveCLI(repoRoot, override string) (string, error) {
	if override != "" {
		if _, err := os.Stat(override); err == nil {
			return filepath.Abs(override)
		}
		return "", fmt.Errorf("no binary at -cli-path=%s", override)
	}
	candidate := filepath.Join(repoRoot, "flex-cli", "build", "flex-convert-cli")
	if _, err := os.Stat(candidate); err == nil {
		return filepath.Abs(candidate)
	}
	return "", fmt.Errorf(
		"flex-convert-cli not found at %s -- build it first:\n  cd flex-cli && mkdir -p build && cd build && cmake .. && make",
		candidate,
	)
}

// buildGoBinary runs `go build` for a Go service so the smoke test always
// exercises the current source, not a stale binary from a previous run.
func buildGoBinary(dir, name string) (string, error) {
	out := filepath.Join(dir, name)
	cmd := exec.Command("go", "build", "-o", out, "main.go")
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return out, nil
}

func waitForPort(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		lastErr = err
		time.Sleep(200 * time.Millisecond)
	}
	return lastErr
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target)
	})
}

// Helper functions

func prefixWriter(prefix string) io.Writer {
	return &lineWriter{prefix: prefix}
}

type lineWriter struct{ prefix string }

func (l *lineWriter) Write(p []byte) (int, error) {
	fmt.Print(l.prefix, string(p))
	return len(p), nil
}

func step(format string, args ...any) {
	fmt.Printf("\n==> "+format+"\n", args...)
}

func ok(format string, args ...any) {
	fmt.Printf("    ok: "+format+"\n", args...)
}
