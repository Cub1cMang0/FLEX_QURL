// flex-image-viewer: a secure, isolated microservice for rendering converted images.

package main

import (
	"fmt"
	"html"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Point relatively to the API's workspace since it's in a different folder
var jobStorePath = "./job_store"
var cliPath = "./flex-convert-cli"

/* jobIDPattern restricts job IDs to a safe, single-path-segment charset.
   This does double duty:
     - no "/" or "\" or ".." means it can never be used for path traversal
       when joined into jobStorePath (arbitrary file read via ?job=../../etc/passwd)
     - no "<", ">", quotes, etc. means it's also safe to drop straight into
       the HTML shell without that on its own enabling reflected XSS
   It's still HTML-escaped again at the point of use as defense in depth. */
var jobIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func isValidJobID(jobID string) bool {
	return jobIDPattern.MatchString(jobID)
}

// A set of formats that modern web browsers is capable of natively displaying
var webSafeFormats = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".bmp": true, ".ico": true, ".jfif": true,
}

// viewHandler serves the minimal HTML shell
func viewHandler(w http.ResponseWriter, r *http.Request) {
	jobID := r.URL.Query().Get("job")
	if jobID == "" {
		http.Error(w, "missing job ID", http.StatusBadRequest)
		return
	}
	if !isValidJobID(jobID) {
		http.Error(w, "invalid job ID", http.StatusBadRequest)
		return
	}
	// Escaped again here even though isValidJobID already restricts the
	// charset -- belt and suspenders against this becoming a reflected-XSS
	// sink if the allowed charset ever changes.
	safeJobID := html.EscapeString(jobID)

	// Serve a minimal HTML page with the embedded image
	page := fmt.Sprintf(`<!DOCTYPE html>
	<html lang="en">
	<head>
	<meta charset="UTF-8">
	<meta name="viewport" content="width=device-width, initial-scale=1.0">
	<title>FLEX — Secure Viewer</title>
	<link href="https://fonts.googleapis.com/css2?family=IBM+Plex+Mono:wght@400;500&family=IBM+Plex+Sans:wght@400;600&display=swap" rel="stylesheet">
	<style>
	body {
		margin: 0;
		background-color: #000000;
		background-image: radial-gradient(circle at 50%% 0%%, #1a1a1a 0%%, transparent 70%%);
		color: #ededed;
		font-family: 'IBM Plex Sans', sans-serif;
		display: flex;
		flex-direction: column;
		align-items: center;
		padding: 64px 20px;
		min-height: 100vh;
	}
	.eyebrow {
		font-family: 'IBM Plex Mono', monospace;
		font-size: 13px;
		color: #888888;
		text-transform: uppercase;
		margin-bottom: 24px;
		letter-spacing: 0.1em;
	}
	.download-btn {
		display: inline-block;
		margin-bottom: 24px;
		background: #0070f3;
		color: #ffffff;
		text-decoration: none;
		padding: 12px 24px;
		border-radius: 6px;
		font-family: 'IBM Plex Mono', monospace;
		font-size: 14px;
		font-weight: 600;
		transition: background 0.2s;
	}
	.download-btn:hover {
		background: #3291ff;
	}
	.viewer-box {
		background: #0a0a0a;
		border: 1px solid #222222;
		border-radius: 12px;
		padding: 24px;
		box-shadow: 0 8px 30px rgba(0, 0, 0, 0.4);
		max-width: 90vw;
	}
	img {
		max-width: 100%%;
		height: auto;
		border-radius: 4px;
		display: block;
	}
	</style>
	</head>
	<body>
	<div class="eyebrow">Zero-Trust Image Viewer</div>
	
	<!-- NEW: Download Button pointing to the raw handler with a dl=true flag -->
	<a href="/raw?job=%s&dl=true" class="download-btn">DOWNLOAD ORIGINAL</a>
	
	<div class="viewer-box">
		<img src="/raw?job=%s" alt="Converted Document">
	</div>
	</body>
	</html>`, safeJobID, safeJobID)

	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(page))
}

// rawHandler fetches the actual image bytes and handles on-the-fly transcoding
func rawHandler(w http.ResponseWriter, r *http.Request) {
	// Fetch job ID
	jobID := r.URL.Query().Get("job")
	/* Validate before it ever touches the filesystem -- without this, a
	   request like ?job=../../../etc could join into a path outside
	   jobStorePath and read (or, via the transcode branch, even attempt to
	   convert) arbitrary files on disk. */
	if !isValidJobID(jobID) {
		http.Error(w, "invalid job ID", http.StatusBadRequest)
		return
	}
	workDir := filepath.Join(jobStorePath, jobID)
	// Scan the job directory for the converted image
	files, err := os.ReadDir(workDir)
	if err != nil {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}
	var originalFile string
	var pngFile string
	for _, f := range files {
		// Ignore the JSON prefs file
		if filepath.Ext(f.Name()) == ".json" {
			continue
		}
		// Categorize the files we find
		if strings.ToLower(filepath.Ext(f.Name())) == ".png" {
			pngFile = f.Name()
		} else {
			originalFile = f.Name()
		}
	}
	// Used to determine whether or not the user converted to a non natively supported extension or just a png
	targetFile := originalFile
	if targetFile == "" {
		targetFile = pngFile
	}
	if targetFile == "" {
		http.Error(w, "no image found in job store", http.StatusNotFound)
		return
	}
	// Extract extension 
	ext := strings.ToLower(filepath.Ext(targetFile))
	// Construct full file path
	fullPath := filepath.Join(workDir, targetFile)
	// Used to download the converted image output
	if r.URL.Query().Get("dl") == "true" {
		// Force the browser to download the file instead of displaying it
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, targetFile))
		http.ServeFile(w, r, fullPath)
		return
	}
	// If the browser can render it natively, serve it directly
	if webSafeFormats[ext] {
		http.ServeFile(w, r, fullPath)
		return
	}
	// Construct output file name
	baseName := strings.TrimSuffix(targetFile, filepath.Ext(targetFile))
	// If the browser can't render it (e.g., .pbm, .xbm) transcode it to a PNG for viewing
	transcodePath := filepath.Join(workDir, baseName+".png")
	// Only run the C++ converter if we haven't already generated the web_view.png
	if _, err := os.Stat(transcodePath); os.IsNotExist(err) {
		log.Printf("Format %s not web-safe. Transcoding to PNG...", ext)
		// Construct conversion command from unsupported render extension to png
		cmd := exec.Command(cliPath, fullPath, workDir, "png", "{}")
		cliOut, err := cmd.CombinedOutput()
		if err != nil {
			log.Printf("viewer transcoding failed: %s", string(cliOut))
			// Set error when converting a non natively supported extension to png for viewing
			http.Error(w, "viewer transcoding failed", http.StatusInternalServerError)
			return
		}
	}
	// Serve the newly minted PNG
	http.ServeFile(w, r, transcodePath)
}

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/view", viewHandler)
	mux.HandleFunc("/raw", rawHandler)

	// Explicit timeouts for the same slowloris reasons as flex-web-api.
	srv := &http.Server{
		Addr:              ":8081",
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}
	log.Printf("flex-image-viewer listening on %s", srv.Addr)
	log.Fatal(srv.ListenAndServe())
}