package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

/* A real, valid 2x2 RGBA PNG (verified to decode cleanly).
   Used to test the "web-safe format" path, which never shells out. */
var tinyPNG = []byte{
	0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D,
	0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x02, 0x00, 0x00, 0x00, 0x02,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x72, 0xB6, 0x0D, 0x24, 0x00, 0x00, 0x00,
	0x14, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9C, 0x63, 0xFC, 0xCF, 0xC0, 0xF0,
	0x9F, 0x81, 0x81, 0x81, 0x81, 0x89, 0x01, 0x0A, 0x00, 0x1F, 0x17, 0x02,
	0x02, 0x4F, 0x94, 0xCE, 0xBE, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4E,
	0x44, 0xAE, 0x42, 0x60, 0x82,
}

// Used to resolve pathing issues with the flex-convert-cli
func resolveCLIPath(t *testing.T) string {
	// Set helper
	t.Helper()
	// Attempt to extract env variable for the flex-covnert-cli
	if p := os.Getenv("FLEX_CLI_PATH"); p != "" {
		// Extract metadata about the cli path
		if _, err := os.Stat(p); err == nil {
			// Construct absolute path
			abs, _ := filepath.Abs(p)
			return abs
		}
	}
	// Construct CLI executable path
	candidate := filepath.Join("..", "flex-cli", "build", "flex-convert-cli")
	// Check if info about CLIP path is valid
	if _, err := os.Stat(candidate); err == nil {
		// Construct absolute path
		abs, _ := filepath.Abs(candidate)
		return abs
	}
	return ""
}

/* --- viewHandler: pure logic, no filesystem or subprocess involved ---
   This is the easiest thing in either service to unit test: given a job ID
   string, it deterministically returns an HTML shell. No I/O, no CLI. */

func TestViewHandler_RendersHTMLShell(t *testing.T) {
	// Construct request
	req := httptest.NewRequest(http.MethodGet, "/view?job=job-123", nil)
	// Set recorder
	rec := httptest.NewRecorder()
	// Attempt to serve the raw HTML shell
	viewHandler(rec, req)
	// Define result from recorder
	res := rec.Result()
	// Check if the results showed success
	if res.StatusCode != http.StatusOK {
		// Set a fatal error when the viewing failed
		t.Fatalf("expected 200, got %d", res.StatusCode)
	}
	// Check if result's content type isn't text/html
	if ct := res.Header.Get("Content-Type"); ct != "text/html" {
		// Set error when content type isn't text/html
		t.Errorf("expected Content-Type text/html, got %q", ct)
	}
	body, _ := io.ReadAll(res.Body)
	// Check if content body doesn't have the proper embedding
	if !strings.Contains(string(body), "/raw?job=job-123") {
		// Set error when embedding doesn't match the expected content
		t.Errorf("expected body to embed /raw?job=job-123, got: %s", body)
	}
}

func TestViewHandler_MissingJobID(t *testing.T) {
	// Construct request without job id
	req := httptest.NewRequest(http.MethodGet, "/view", nil)
	// Set recorder
	rec := httptest.NewRecorder()
	// Attempt to serve the raw HTML shell
	viewHandler(rec, req)
	// Check if the recorder doesn't have the proper error code
	if rec.Code != http.StatusBadRequest {
		// Set error when response code doesn't match the expected code
		t.Errorf("expected 400 for missing job ID, got %d", rec.Code)
	}
}

/* --- rawHandler: filesystem + subprocess bound, same as convertHandler ---
   These tests need real files on disk (job store) and, for the transcode
   branch, a real CLI binary. This is exactly as I/O-heavy as the API's
   convertHandler -- it's not "less testable because it's the viewer",
   it's just as I/O-bound as anything that touches job_store or the CLI. */
func TestRawHandler_JobNotFound(t *testing.T) {
	// Define a job directory
	jobStorePath = t.TempDir()
	// Construct a request for a job that doesn't exist
	req := httptest.NewRequest(http.MethodGet, "/raw?job=does-not-exist", nil)
	// Set the recorder
	rec := httptest.NewRecorder()
	// Attempt to fetch the actual image bytes
	rawHandler(rec, req)
	// Check if the recorder doesn't have the proper error code
	if rec.Code != http.StatusNotFound {
		// Set error when response code doesn't match the expected code
		t.Errorf("expected 404 for missing job dir, got %d", rec.Code)
	}
}

func TestRawHandler_NoImageInJobDir(t *testing.T) {
	// Set job directory
	tmp := t.TempDir()
	jobStorePath = tmp
	jobDir := filepath.Join(tmp, "job-empty")
	// Check if directory creation was successful
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		// Set error when creation fails
		t.Fatal(err)
	}
	/* Only a prefs JSON file present -- rawHandler should skip it and
	   report no image found, matching the ".json" exclusion in the source. */
	if err := os.WriteFile(filepath.Join(jobDir, "conversion_preferences.json"), []byte("{}"), 0o644); err != nil {
		// Set error when writing preferences fails
		t.Fatal(err)
	}
	// Construct emtpy job request
	req := httptest.NewRequest(http.MethodGet, "/raw?job=job-empty", nil)
	// Set recorder
	rec := httptest.NewRecorder()
	// Attempt to fetch the actual image bytes
	rawHandler(rec, req)
	// Check if the recorder doesn't have the proper error code
	if rec.Code != http.StatusNotFound {
		// Set error when response code doesn't match the expected code
		t.Errorf("expected 404 when only a .json file is present, got %d", rec.Code)
	}
}

func TestRawHandler_ServesWebSafeFormatDirectly(t *testing.T) {
	tmp := t.TempDir()
	jobStorePath = tmp
	/* Point cliPath somewhere that would fail loudly if ever invoked --
	   proves this branch really doesn't shell out for web-safe formats. */
	cliPath = filepath.Join(tmp, "cli-should-not-be-called")
	// Set job directory
	jobDir := filepath.Join(tmp, "job-png")
	// Ensure job directory creation is successful
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		// Set error if creation fails
		t.Fatal(err)
	}
	// Construct png path
	pngPath := filepath.Join(jobDir, "output.png")
	// Ensure writing png is successful
	if err := os.WriteFile(pngPath, tinyPNG, 0o644); err != nil {
		// Set error when writing fails
		t.Fatal(err)
	}
	// Create png view request
	req := httptest.NewRequest(http.MethodGet, "/raw?job=job-png", nil)
	// Set recorder
	rec := httptest.NewRecorder()
	// Attempt to fetch the actual image bytes
	rawHandler(rec, req)
	// Define results of viewing
	res := rec.Result()
	// Ensure view request is successful
	if res.StatusCode != http.StatusOK {
		// Set a fatal error when viewing fails
		t.Fatalf("expected 200, got %d", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	// Ensure contents aren't touched before being served
	if string(body) != string(tinyPNG) {
		// Set error when this fails
		t.Errorf("expected exact PNG bytes to be served untouched")
	}
}

func TestRawHandler_TranscodesNonWebSafeFormat(t *testing.T) {
	// Fetch flex cli binary
	cli := resolveCLIPath(t)
	if cli == "" {
		// Skip when binary can't be found
		t.Skip("flex-convert-cli binary not found; build flex-cli or set FLEX_CLI_PATH to run this test")
	}
	// Set job path
	tmp := t.TempDir()
	jobStorePath = tmp
	cliPath = cli
	// Construct job path
	jobDir := filepath.Join(tmp, "job-cur")
	// Ensure creation of job directory succeeds
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		// Set a fatal error when creation fails
		t.Fatal(err)
	}
	/* .cur is not in webSafeFormats, so rawHandler should shell out to the
	   CLI and transcode it to a PNG before serving it. */
	curFixture := os.Getenv("FLEX_CUR_FIXTURE")
	if curFixture == "" {
		// Skip when setting the curFixture fails
		t.Skip("set FLEX_CUR_FIXTURE to a sample .cur file to run this test")
	}
	// Read file from curFixture
	curBytes, err := os.ReadFile(curFixture)
	if err != nil {
		// Set a fatal error when reading fails
		t.Fatalf("could not read fixture at FLEX_CUR_FIXTURE=%s: %v", curFixture, err)
	}
	// Construct cur file path
	curPath := filepath.Join(jobDir, "output.cur")
	// Ensure file creation succeeds
	if err := os.WriteFile(curPath, curBytes, 0o644); err != nil {
		// Set a fatal error when writing fails
		t.Fatal(err)
	}
	// Construct a cur viewing request
	req := httptest.NewRequest(http.MethodGet, "/raw?job=job-cur", nil)
	// Set recorder
	rec := httptest.NewRecorder()
	// Attempt to view the actual image bytes
	rawHandler(rec, req)
	// Define results
	res := rec.Result()
	// Ensure results are successful
	if res.StatusCode != http.StatusOK {
		// Set a fatal error when viewing of a cur file fails
		t.Fatalf("expected 200 after transcode, got %d: %s", res.StatusCode, rec.Body.String())
	}
	body, _ := io.ReadAll(res.Body)
	// Ensure transcoding is a png
	if len(body) < 8 || string(body[1:4]) != "PNG" {
		// Set error when transcoded response isn't a png
		t.Errorf("expected transcoded response to be a PNG, got %d bytes starting %v", len(body), body[:min(8, len(body))])
	}
	// Construct the transcoded png path
	transcodePath := filepath.Join(jobDir, "output.png")
	// Ensure file info gathering is successful
	if _, err := os.Stat(transcodePath); err != nil {
		// Set error when png isn't cached
		t.Errorf("expected transcoded file to be cached at %s: %v", transcodePath, err)
	}

	/* Second request should reuse the cached transcode. Prove it by pointing cliPath at something 
	   broken -- if the handler tried to shell out again, this would fail. */
	cliPath = filepath.Join(tmp, "this-binary-does-not-exist")
	rec2 := httptest.NewRecorder()
	rawHandler(rec2, httptest.NewRequest(http.MethodGet, "/raw?job=job-cur", nil))
	if rec2.Code != http.StatusOK {
		// Set error when CLI is reinvoked
		t.Errorf("expected cached transcode to be served without re-invoking the CLI, got %d", rec2.Code)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}