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
	"encoding/json"
)


// Define cli executable location
var cliPath = "./flex-convert-cli"

// Define the location to store converted image jobs
var jobPath = "./job_store"

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
	/* Generate a unique job ID based on the current Unix nanosecond timestamp. This is to avoid
	any ID generation based on file characteristics */
	jobID := fmt.Sprintf("job-%d", time.Now().UnixNano())
	// Set temp working directory for conversion
	workDir := filepath.Join(jobPath, jobID)
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		// Set error if creating output directory in the job_store directory fails
		http.Error(w, "server error creating workspace", http.StatusInternalServerError)
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
		// Close directory and set error if writing fails
		dst.Close()
		http.Error(w, "server error writing upload: "+err.Error(), http.StatusInternalServerError)
		return
	}
	dst.Close()
	// Create temp preference json object 
	prefsJSON := r.FormValue("preferences")
	if prefsJSON == "" {
		prefsJSON = "{}"
	}
	// Get the absolute path to the executable before changing directories
    absCliPath, err := filepath.Abs(cliPath)
    if err != nil {
		// Set error if creating absolute path fails
        http.Error(w, "server error resolving CLI path", http.StatusInternalServerError)
        return
    }
	/* context.WithTimeout gives the subprocess a hard deadline
     flex-convert-cli hangs, this request fails instead of hanging forever. */
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	// Construct command to run FLEX
	cmd := exec.CommandContext(ctx, absCliPath, inputPath, workDir, outputExt, prefsJSON)
	log.Printf("abs: %s, input: %s, output: %s, ext: %s, prefs: %s", absCliPath, inputPath, workDir, outputExt, string(prefsJSON))
	cliOutput, err := cmd.CombinedOutput()
	if err != nil {
		// Set error if command construction fails
		http.Error(w, fmt.Sprintf("conversion failed: %s", strings.TrimSpace(string(cliOutput))), http.StatusUnprocessableEntity)
		return
	}
	os.Remove(inputPath)
	log.Printf("cli: %s", strings.TrimSpace(string(cliOutput)))
	// Set header info
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	response := map[string]string{
		"status": "success",
		"job_id": jobID,
	}
	json.NewEncoder(w).Encode(response)
}

func main() {
	// Ensure the persistent job store exists when the server starts
	if err := os.MkdirAll(jobPath, 0o755); err != nil {
		log.Fatalf("Failed to create job store directory: %v", err)
	}
	http.HandleFunc("/convert", convertHandler)
	// Serve the front-end (index.html, etc.) from ./static
	http.Handle("/", http.FileServer(http.Dir("./static")))
	addr := ":8080"
	log.Printf("flex-web-api listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
