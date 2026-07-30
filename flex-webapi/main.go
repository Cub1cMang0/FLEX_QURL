// flex-web-api: a small HTTP service that wraps flex-convert-cli.

package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)


// Define cli executable location
const cliPath = "./flex-convert-cli"

func convertHandler(w http.ResponseWriter, r *http.Request) {
	// Ensure method is POST
	if r.Method != http.MethodPost {
		http.Error(w, "use POST", http.StatusMethodNotAllowed)
		return
	}
	// Target format comes from a query string, e.g. POST /convert?to=ico
	outputExt := r.URL.Query().Get("to")
	if outputExt == "" {
		// Set error
		http.Error(w, "missing 'to' query param, e.g. ?to=ico", http.StatusBadRequest)
		return
	}
	// Ensure proper parsing of upload 
	if err := r.ParseMultipartForm(32 << 20); err != nil { // 32 MiB
		// Set error
		http.Error(w, "could not parse upload: "+err.Error(), http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		// Set error if FormFile fails
		http.Error(w, "expected a multipart file field named 'file': "+err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()
	// Set temp working directory for conversion
	workDir, err := os.MkdirTemp("", "flex-job-*")
	if err != nil {
		// Set error if temp directory creation fails
		http.Error(w, "server error creating workspace", http.StatusInternalServerError)
		return
	}
	defer os.RemoveAll(workDir)
	// Create output directory
	outDir := filepath.Join(workDir, "out")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		// Set error if output directory creation fails
		http.Error(w, "server error creating output dir", http.StatusInternalServerError)
		return
	}
	// Construct input path
	inputPath := filepath.Join(workDir, header.Filename)
	dst, err := os.Create(inputPath)
	if err != nil {
		// Set error if output directory creation fails
		http.Error(w, "server error saving upload", http.StatusInternalServerError)
		return
	}
	if _, err := io.Copy(dst, file); err != nil {
		// close directory and set error if writing fails
		dst.Close()
		http.Error(w, "server error writing upload: "+err.Error(), http.StatusInternalServerError)
		return
	}
	dst.Close()
	// context.WithTimeout gives the subprocess a hard deadline
	// flex-convert-cli hangs, this request fails instead of hanging forever.
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	prefsJSON := r.FormValue("preferences")
	if prefsJSON == "" {
		prefsJSON = "{}"
	}
	// Construct command to run FLEX
	log.Printf("%s", prefsJSON)
	cmd := exec.CommandContext(ctx, cliPath, inputPath, outDir, outputExt, prefsJSON)
	cliOutput, err := cmd.CombinedOutput()
	if err != nil {
		// Set error if command construction fails
		http.Error(w, fmt.Sprintf("conversion failed: %s", strings.TrimSpace(string(cliOutput))), http.StatusUnprocessableEntity)
		return
	}
	log.Printf("cli: %s", strings.TrimSpace(string(cliOutput)))
	// Extract base file name
	base := strings.TrimSuffix(header.Filename, filepath.Ext(header.Filename))
	// Construct output file name
	resultName := base + "." + strings.ToLower(outputExt)
	// Construct output path
	resultPath := filepath.Join(outDir, resultName)
	// Attempt to open output
	resultFile, err := os.Open(resultPath)
	if err != nil {
		// Set error if output opening fails
		http.Error(w, "conversion reported success but output file was not found", http.StatusInternalServerError)
		return
	}
	defer resultFile.Close()
	// Set header info
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, resultName))
	// Copy file
	io.Copy(w, resultFile)
}

func main() {
	http.HandleFunc("/convert", convertHandler)
	// Serve the front-end (index.html, etc.) from ./static
	http.Handle("/", http.FileServer(http.Dir("./static")))
	addr := ":8080"
	log.Printf("flex-web-api listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
