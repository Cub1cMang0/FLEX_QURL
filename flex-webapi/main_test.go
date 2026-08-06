package main

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

/* A real, valid 2x2 RGBA PNG (verified to decode cleanly), used as the
   upload fixture for the happy-path tests. Hand-rolled PNG bytes are easy
   to get subtly wrong (bad zlib stream, wrong CRC) and will fail inside
   the CLI with a confusing libpng error rather than a clear test failure --
   this one was generated with Pillow and confirmed round-trippable. */
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

/* buildUploadRequest constructs a multipart/form-data POST like a real
  browser upload would, so the handler is exercised through the same
   parsing path (r.ParseMultipartForm / r.FormFile) it uses in production. */
func buildUploadRequest(t *testing.T, url, fieldName, filename string, fileBytes []byte, extraFields map[string]string) *http.Request {
	// Set helper
	t.Helper()
	// Declare byte buffer
	var buf bytes.Buffer
	// Define a new writer using the byte buffer
	w := multipart.NewWriter(&buf)
	if fileBytes != nil {
		// Attempt to create the form file utilizng the field and file name provided
		fw, err := w.CreateFormFile(fieldName, filename)
		if err != nil {
			// Return a fatal error if creation fails
			t.Fatal(err)
		}
		// Attempt to write bytes to the form file utilizing the file bytes provided
		if _, err := fw.Write(fileBytes); err != nil {
			// return a fatal error if writing fails
			t.Fatal(err)
		}
	}
	for k, v := range extraFields {
		// Use writer for key value pairs from the extra fields
		if err := w.WriteField(k, v); err != nil {
			// Ser fatal error if there is an issue with a key and or value
			t.Fatal(err)
		}
	}
	// Attempt to close the writer
	if err := w.Close(); err != nil {
		// Set fatal error if closing fails
		t.Fatal(err)
	}
	// Construct request utilzing url and byte buffer
	req := httptest.NewRequest(http.MethodPost, url, &buf)
	// Set header
	req.Header.Set("Content-Type", w.FormDataContentType())
	return req
}

// --- Pure input-validation branches: no CLI or real job store needed ---

func TestConvertHandler_MethodNotAllowed(t *testing.T) {
	// Construct request with an unallowed method
	req := httptest.NewRequest(http.MethodGet, "/convert?to=png", nil)
	// Define recorder 
	rec := httptest.NewRecorder()
	// Attempt the conversion
	convertHandler(rec, req)
	// Check if the recorder received the proper error code
	if rec.Code != http.StatusMethodNotAllowed {
		// Set error if recorder report the incorrect error code
		t.Errorf("expected 405 for GET, got %d", rec.Code)
	}
}

func TestConvertHandler_MissingToParam(t *testing.T) {
	// Construct upload request with missing to parameter
	req := buildUploadRequest(t, "/convert", "file", "sample.png", tinyPNG, nil)
	// Define recorder
	rec := httptest.NewRecorder()
	// Attempt the conversion
	convertHandler(rec, req)
	// Check if the recorder received the proper error code
	if rec.Code != http.StatusBadRequest {
		// Set error if recorder reports a missing parameter
		t.Errorf("expected 400 for missing 'to' param, got %d", rec.Code)
	}
}

func TestConvertHandler_MissingFileField(t *testing.T) {
	// Construct upload request with a missing field name
	req := buildUploadRequest(t, "/convert?to=ico", "wrong_field_name", "sample.png", tinyPNG, nil)
	// Define recorder
	rec := httptest.NewRecorder()
	// Attempt the conversion
	convertHandler(rec, req)
	// Check if the recorder received the proper error code
	if rec.Code != http.StatusBadRequest {
		// Set error code if recorder reports a missing field name
		t.Errorf("expected 400 when 'file' field is absent, got %d", rec.Code)
	}
}

// --- Full round trip: needs a real job store dir and a real CLI binary ---

func TestConvertHandler_SuccessAndCleansUpRawInput(t *testing.T) {
	// Fetch flex cli binary
	cli := resolveCLIPath(t)
	if cli == "" {
		// Skip when binary can't be found
		t.Skip("flex-convert-cli binary not found; build flex-cli or set FLEX_CLI_PATH to run this test")
	}
	cliPath = cli
	// Set a job directory
	jobPath = t.TempDir()
	// Construct request using basic png defined above
	req := buildUploadRequest(t, "/convert?to=ico", "file", "sample.png", tinyPNG, map[string]string{
		"preferences": "{}",
	})
	// Define recorder
	rec := httptest.NewRecorder()
	// Attempt the conversion
	convertHandler(rec, req)
	// Store results of conversion
	res := rec.Result()
	// Check if conversion didn't succeed
	if res.StatusCode != htFtp.StatusOK {
		body, _ := io.ReadAll(res.Body)
		// Set a fatal error when conversion fails
		t.Fatalf("expected 200, got %d: %s", res.StatusCode, body)
	}
	var parsed map[string]string
	// Check for response validity
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		t.Fatalf("response was not valid JSON: %v", err)
	}
	// Check for status
	if parsed["status"] != "success" {
		t.Errorf("expected status=success, got %q", parsed["status"])
	}
	// Extract job id
	jobID := parsed["job_id"]
	if jobID == "" {
		// Set a fatal error when the extracted job id is empty
		t.Fatal("expected a non-empty job_id in the response")
	}
	// Verify the job directory actually holds the converted output.
	jobDir := filepath.Join(jobPath, jobID)
	entries, err := os.ReadDir(jobDir)
	if err != nil {
		// Set a fatal error when the job directory does not exist
		t.Fatalf("expected job directory %s to exist: %v", jobDir, err)
	}
	var rawInputStillPresent, convertedOutputPresent bool
	for _, e := range entries {
		// Check if the sample png is still present after conversion
		if e.Name() == "sample.png" {
			rawInputStillPresent = true
		}
		// Check if the converted output is present in the job directory
		if filepath.Ext(e.Name()) == ".ico" {
			convertedOutputPresent = true
		}
	}
	if rawInputStillPresent {
		// Set error when the input from the conversionis still present
		t.Errorf("expected raw uploaded input to be deleted after conversion, but sample.png is still present")
	}
	if !convertedOutputPresent {
		// Set error when conversion doesn't yield the expected output
		t.Errorf("expected a .ico file in %s, got entries: %v", jobDir, entries)
	}
}

func TestConvertHandler_InvalidImageDataFailsConversion(t *testing.T) {
	// Fetch the flex cli binary
	cli := resolveCLIPath(t)
	if cli == "" {
		// Skip when binary can't be found
		t.Skip("flex-convert-cli binary not found; build flex-cli or set FLEX_CLI_PATH to run this test")
	}
	cliPath = cli
	// Set a job directory
	jobPath = t.TempDir()
	// Set a faux image
	garbage := []byte("this is not a real image")
	// Construct a request with a bad image
	req := buildUploadRequest(t, "/convert?to=png", "file", "not-an-image.png", garbage, nil)
	// Set recorder
	rec := httptest.NewRecorder()
	// Attempt the conversion
	convertHandler(rec, req)
	// Check if error code is incorrect
	if rec.Code != http.StatusUnprocessableEntity {
		// Set error when error code doesn't match the expected code
		t.Errorf("expected 422 for unconvertible input, got %d: %s", rec.Code, rec.Body.String())
	}
}