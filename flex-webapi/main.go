// flex-web-api: a small HTTP service that wraps flex-convert-cli.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"flex-web-api/storage"
	"github.com/joho/godotenv"
)

// Define cli executable location
var cliPath = "./flex-convert-cli"

// Define the location to store converted image jobs
var jobPath = "./job_store"

// Define max byte upload limit to 32 MiB
var maxUploadBytes int64 = 32 << 20

func init() {
	// Check to see if the env file isn't empty
	if v := os.Getenv("MAX_UPLOAD_BYTES"); v != "" {
		// Parse set value from env
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			// Set the new maxUploadBytes if the extracted number from the env is valid
			maxUploadBytes = n
		} else {
			// Print out message indicating a lack of env
			log.Printf("Using default maxUploadBytes (no env found): %q", maxUploadBytes)
		}
	}
}

// Define a whitelist of supported extension to reject unsupported extensions before reaching the CLI
var supportedExts = map[string]bool{
	"png": true, "jpeg": true, "jpg": true, "ico": true, "jfif": true,
	"pbm": true, "pgm": true, "ppm": true, "bmp": true, "cur": true,
	"xbm": true, "xpm": true,

}

// Define a whitelist of extensions that can be natively viewed in browsers
var webSafeFormats = map[string]bool{
	"png": true, "jpg": true, "jpeg": true, "bmp": true, "ico": true, "jfif": true,
}

// Used to strip any directory pathing information to just return the file name rather than the whole path
func sanitizeFilename(name string) string {
	baseName := filepath.Base(name)
	// Check for any valid file names
	if baseName == "." || baseName == ".." || baseName == "" || baseName == string(filepath.Separator) {
		return "upload"
	}
	return baseName
}

// Used to reduce the possibility of two job generating the same job id (rare but still possible)
var jobSeq int64

// Used to set the job id utilizing the atomic counter to append to the UnixNano-based job id.
func nextJobID() string {
	seq := atomic.AddInt64(&jobSeq, 1)
	return fmt.Sprintf("job-%d-%d", time.Now().UnixNano(), seq)
}

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
	// Ensure that the output extension is supported
	if !supportedExts[outputExt] {
		// Set an error when an unsupported extension is provided
		http.Error(w, fmt.Sprintf("Unsupported target format %q", outputExt), http.StatusBadRequest)
		return
	}
	// Wrap request body to avoid handing over bytes exceeding maxUploadBytes limit
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	// Ensure proper parsing of upload 
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		var mbErr *http.MaxBytesError
		if errors.As(err, &mbErr) {
			// Set an error when the user attempts to upload a file larger than 32 MiB
			http.Error(w, "Upload exceeds maximum allowed size", http.StatusRequestEntityTooLarge)
			return
		}
		// Set error when the upload just fails even if the upload meet the requirements
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
	/* Generate a unique job ID based on the current Unix nanosecond timestamp and atomic sequence number. 
	This is to avoid any ID generation based on file characteristics and collisions */
	jobID := nextJobID()
	// Set temp working directory for conversion
	workDir := filepath.Join(jobPath, jobID)
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		// Set error if creating output directory in the job_store directory fails
		http.Error(w, "server error creating workspace", http.StatusInternalServerError)
		return
	}
	// Construct input path
	inputPath := filepath.Join(workDir, sanitizeFilename(header.Filename))
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
	cliOutput, err := cmd.CombinedOutput()
	if err != nil {
		// Set error if command construction fails
		http.Error(w, fmt.Sprintf("conversion failed: %s", strings.TrimSpace(string(cliOutput))), http.StatusUnprocessableEntity)
		return
	}
	os.Remove(inputPath)
	// Set up R2 client to begin upload
	r2Client, err := storage.NewR2Client(ctx)
	if err != nil {
		// Set error if command construction fails
		http.Error(w, "Server error attempting to connect to Cloudflare R2", http.StatusInternalServerError)
		return
	}
	outputPath := filepath.Join(workDir, jobID, ".", outputExt)
	downloadPath, downloadURL, err := r2Client.UploadJobImage(ctx, jobID, outputPath, "." + outputExt)
	if err != nil {
		http.Error(w, "Server error attempting to download converted output", http.StatusInternalServerError)
		return
	}
	var viewPath string
	var viewURL string
	var e error
	if !webSafeFormats[outputExt] {
		fileName := fmt.Sprintf("%s.%s", jobID, outputExt)
		unsafeFilePath := filepath.Join(workDir, fileName)
		cmd := exec.CommandContext(ctx, absCliPath, unsafeFilePath, workDir, "png", "{}")
		cliOutput, err := cmd.CombinedOutput()
		if err != nil {
			http.Error(w, fmt.Sprintf("conversion failed: %s", strings.TrimSpace(string(cliOutput))), http.StatusUnprocessableEntity)
			return
		}
		outputPath = filepath.Join(workDir, jobID, ".png")
		downloadPath, _, err = r2Client.UploadJobImage(ctx, jobID, outputPath, ".png")
		if err != nil {
			http.Error(w, "Server error attempting to download converted output", http.StatusInternalServerError)
			return
		}
		viewPath, viewURL, e = r2Client.CreateViewCopy(ctx, downloadPath, ".png")
		if e != nil {
			http.Error(w, "Server error attempting to view converted output", http.StatusInternalServerError)
			return
		}
	} else {
		viewPath, viewURL, err = r2Client.CreateViewCopy(ctx, downloadPath, "." + outputExt)
		if err != nil {
			http.Error(w, "Server error attempting to view converted output", http.StatusInternalServerError)
			return
		}
	}
	metaData := map[string]string{
		"jobID": jobID,
		"downloadURL": downloadURL,
		"viewURL": viewURL,
		"download_CRID": "",
		"view_CRID": "",
	}
	jsonData, err := json.Marshal(metaData)
	if err != nil {
		http.Error(w, "Server error attempting to create meta data", http.StatusInternalServerError)
		return
	}
	metaPath := filepath.Join(workDir, "meta.json")
	err = os.WriteFile(metaPath, jsonData, 0644)
	if err != nil {
		http.Error(w, "Server error attempting to write meta data", http.StatusInternalServerError)
		return
	}
	err = r2Client.UploadMetadata(ctx, metaPath, jobID)
	if err != nil {
		http.Error(w, "Unable to upload meta data", http.StatusInternalServerError)
		return
	}
	fmt.Printf("download image %s: ", downloadPath)
	fmt.Printf("view image %s: ", viewPath)
	log.Printf("cli: %s", strings.TrimSpace(string(cliOutput)))
	// Set header info
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	response := map[string]string{
		"status": "success",
		"jobID": jobID,
		"filename": sanitizeFilename(header.Filename),
	}
	json.NewEncoder(w).Encode(response)
}

func viewHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "use GET", http.StatusMethodNotAllowed)
		return
	}
	ctx, _ := context.WithTimeout(r.Context(), 30*time.Second)
	// Set up R2 client to begin upload
	r2Client, err := storage.NewR2Client(ctx)
	if err != nil {
		// Set error if command construction fails
		http.Error(w, "Server error attempting to connect to Cloudflare R2", http.StatusInternalServerError)
		return
	}
	jobID := r.PathValue("jobID")
	jsonData, err := r2Client.GetMetadata(ctx, jobID)
	if err != nil {
		http.Error(w, "Server error attempting to get meta data", http.StatusInternalServerError)
		return
	}
	viewURL := jsonData["viewURL"]
	cmd := exec.Command("qurl", "publish", viewURL)
	crid, err := cmd.CombinedOutput()
	if err != nil {
		http.Error(w, "Server error attempting to publish resource", http.StatusInternalServerError)
		return
	}
	cmd = exec.Command("qurl", "resolve", string(crid))
	qurlLink, err := cmd.CombinedOutput()
	if err != nil {
		http.Error(w, "Server error attempting to resolve resource", http.StatusUnprocessableEntity)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	response := map[string]string{
		"status": "success",
		"qurl_link": string(qurlLink),
	}
	json.NewEncoder(w).Encode(response)
}

func downloadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "use GET", http.StatusMethodNotAllowed)
		return
	}
	ctx, _ := context.WithTimeout(r.Context(), 30*time.Second)
	// Set up R2 client to begin upload
	r2Client, err := storage.NewR2Client(ctx)
	if err != nil {
		// Set error if command construction fails
		http.Error(w, "Server error attempting to connect to Cloudflare R2", http.StatusInternalServerError)
		return
	}
	jobID := r.PathValue("jobID")
	jsonData, err := r2Client.GetMetadata(ctx, jobID)
	if err != nil {
		http.Error(w, "Server error attempting to get meta data", http.StatusInternalServerError)
		return
	}
	downloadURL := jsonData["downloadURL"]
	cmd := exec.Command("qurl", "publish", downloadURL)
	if err != nil {
		http.Error(w, "Error attempting to construct publish command", http.StatusInternalServerError)
		return
	}
	crid, err := cmd.CombinedOutput()
	if err != nil {
		http.Error(w, "Server error attempting to publish resource", http.StatusInternalServerError)
		return
	}
	cmd = exec.Command("qurl", "resolve", string(crid))
	if err != nil {
		http.Error(w, "Server error attempting to construct resolve command", http.StatusUnprocessableEntity)
		return
	}
	qurlLink, err := cmd.CombinedOutput()
	if err != nil {
		http.Error(w, "Server error attempting to resolve resource", http.StatusUnprocessableEntity)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	response := map[string]string{
		"status": "success",
		"qurl_link": string(qurlLink),
	}
	json.NewEncoder(w).Encode(response)
}

/* sweepStaleJobs removes job directories under root whose most recent
   modification time is older than maxAge. It's written as a pure-ish
   function (root/maxAge/now all passed in, no ticker or global state) so it
   can be unit tested directly instead of only through a real hour-long
   ticker. Returns the number of job directories removed. */
func sweepStaleJobs(root string, maxAge time.Duration, now time.Time) (removed int, err error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			// job_store should only ever contain per-job subdirectories;
			// skip anything else rather than deleting it.
			continue
		}
		info, err := e.Info()
		if err != nil {
			log.Printf("sweeper: could not stat %s: %v", e.Name(), err)
			continue
		}
		if now.Sub(info.ModTime()) > maxAge {
			path := filepath.Join(root, e.Name())
			if err := os.RemoveAll(path); err != nil {
				log.Printf("sweeper: failed to remove %s: %v", path, err)
				continue
			}
			removed++
		}
	}
	return removed, nil
}
 
// startJobStoreSweeper runs sweepStaleJobs on a ticker for the lifetime of
// the process. This is the fix for "what happens to job_store after 10,000
// conversions": left alone it grows forever, so this sweeps it every
// interval and deletes anything older than maxAge.
func startJobStoreSweeper(root string, interval, maxAge time.Duration) {
	ticker := time.NewTicker(interval)
	go func() {
		for range ticker.C {
			removed, err := sweepStaleJobs(root, maxAge, time.Now())
			if err != nil {
				log.Printf("sweeper: error reading job store %s: %v", root, err)
				continue
			}
			if removed > 0 {
				log.Printf("sweeper: removed %d stale job(s) older than %s", removed, maxAge)
			}
		}
	}()
}


func main() {
	// Load in .env variables from either a local .env or env variables set on Render.
	if err := godotenv.Load(); err != nil {
		fmt.Printf("warning: could not load .env variables: %v\n", err)
	}
	// Ensure the persistent job store exists when the server starts
	if err := os.MkdirAll(jobPath, 0o755); err != nil {
		log.Fatalf("Failed to create job store directory: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/convert", convertHandler)
	mux.HandleFunc("GET /jobs/{jobID}/view", viewHandler)
	mux.HandleFunc("GET /jobs/{jobID}/download", downloadHandler)
	// Serve the front-end (index.html, etc.) from ./static
	mux.Handle("/", http.FileServer(http.Dir("./static")))
 
	/* Explicit timeouts instead of the bare http.ListenAndServe default,
	   which has none -- a client that trickles bytes in slowly (slowloris)
	   or never sends them can otherwise hold a connection open forever. */
	srv := &http.Server{
		Addr:              ":8080",
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}
	log.Printf("flex-web-api listening on %s", srv.Addr)
	log.Fatal(srv.ListenAndServe())
}

