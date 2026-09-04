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
	// Capture output and error
	cliOutput, err := cmd.CombinedOutput()
	if err != nil {
		// Set error if command construction fails
		http.Error(w, fmt.Sprintf("conversion failed: %s", strings.TrimSpace(string(cliOutput))), http.StatusUnprocessableEntity)
		return
	}
	os.Remove(inputPath)
	// Get the original sanitized filename and strip its original extension
	sanitizedName := sanitizeFilename(header.Filename)
	baseName := strings.TrimSuffix(sanitizedName, filepath.Ext(sanitizedName))
	// Construct the actual path the CLI wrote to
	outputPath := filepath.Join(workDir, baseName + "." + outputExt)
	// Set up R2 client to begin upload
	r2Client, err := storage.NewR2Client(ctx)
	if err != nil {
		// Set error if command construction fails
		http.Error(w, "Server error attempting to connect to Cloudflare R2", http.StatusInternalServerError)
		return
	}
	// Upload image to Cloudflare R2 storage
	downloadPath, downloadURL, err := r2Client.UploadJobImage(ctx, jobID, outputPath, "." + outputExt)
	if err != nil {
		http.Error(w, "Server error attempting to download converted output", http.StatusInternalServerError)
		return
	}
	// Set up view variables to ensure that the viewing link will be viewable regardless of native browser support 
	var viewPath string
	var viewURL string
	var e error
	// Check if extension is already supported
	if !webSafeFormats[outputExt] {
		// Construct output of unsafe extension that was converted
		unsafeFilePath := outputPath
		// Convert unsafe extension to a png (since it's viewable and yeah)
		cmd := exec.CommandContext(ctx, absCliPath, unsafeFilePath, workDir, "png", "{}")
		// Capture output and error
		cliOutput, err := cmd.CombinedOutput()
		if err != nil {
			http.Error(w, fmt.Sprintf("conversion failed: %s", strings.TrimSpace(string(cliOutput))), http.StatusUnprocessableEntity)
			return
		}
		// Construct png local disk location
		pngOutputPath := filepath.Join(workDir, baseName + ".png")
		// Upload png to Cloudflare R2
		tmpDownloadPath, _, err := r2Client.UploadJobImage(ctx, jobID, pngOutputPath, ".png")
		if err != nil {
			http.Error(w, "Server error attempting to download converted output", http.StatusInternalServerError)
			return
		}
		// Create a copy that is for viewing purposes (the uploading results in download link)
		viewPath, viewURL, e = r2Client.CreateViewCopy(ctx, tmpDownloadPath, ".png")
		if e != nil {
			http.Error(w, "Server error attempting to view converted output", http.StatusInternalServerError)
			return
		}
		// Delete older png
		_ = r2Client.DeleteJobImage(ctx, jobID, ".png")
	} else {
		// Create a viewable link for the image since it has native browser support
		viewPath, viewURL, err = r2Client.CreateViewCopy(ctx, downloadPath, "." + outputExt)
		if err != nil {
			http.Error(w, "Server error attempting to view converted output", http.StatusInternalServerError)
			return
		}
	}
	// Construct meta data to be used later for a given job (sweeping, sharing, reusing CRIDS, etc)
	metaData := map[string]string{
		"jobID": jobID,
		"createdAt": time.Now().UTC().Format(time.RFC3339),
		"downloadURL": downloadURL,
		"viewURL": viewURL,
		"download_CRID": "",
		"view_CRID": "",
	}
	// Create meta data into a json objec
	jsonData, err := json.Marshal(metaData)
	if err != nil {
		http.Error(w, "Server error attempting to create meta data", http.StatusInternalServerError)
		return
	}
	// Construct meta.json location
	metaPath := filepath.Join(workDir, "meta.json")
	// Write json file to local disk for uploading
	err = os.WriteFile(metaPath, jsonData, 0644)
	if err != nil {
		http.Error(w, "Server error attempting to write meta data", http.StatusInternalServerError)
		return
	}
	// Upload meta.json to its respective job directory
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
	// Ensure proper http method before beginning
	if r.Method != http.MethodGet {
		http.Error(w, "use GET", http.StatusMethodNotAllowed)
		return
	}
	// Set up context
	ctx, _ := context.WithTimeout(r.Context(), 30*time.Second)
	// Set up R2 client to begin upload
	r2Client, err := storage.NewR2Client(ctx)
	if err != nil {
		// Set error if command construction fails
		http.Error(w, "Server error attempting to connect to Cloudflare R2", http.StatusInternalServerError)
		return
	}
	// Extract job ID from view button request
	jobID := r.PathValue("jobID")
	// Fetch meta.json from the specified job id directory
	jsonData, err := r2Client.GetMetadata(ctx, jobID)
	if err != nil {
		http.Error(w, "Server error attempting to get meta data", http.StatusInternalServerError)
		return
	}
	// Set crid variable
	var crid string
	// Check if there isn't already a CRID to share
	if jsonData["view_CRID"] == "" {
		// Extract viewing URL
		viewURL := jsonData["viewURL"]
		// Construct publishing command with -q to ensure only the generated CRID is captured
		publishCmd := exec.Command("qurl", "publish", "-q", viewURL)
		// Capture output and error
		cridBytes, err := publishCmd.CombinedOutput()
		if err != nil {
			http.Error(w, "Server error attempting to publish resource", http.StatusInternalServerError)
			return
		}
		// Extract CRID
		crid = strings.TrimSpace(string(cridBytes))
		// Set CRID
		jsonData["view_CRID"] = crid
		// Set directory of job id
		workDir := filepath.Join(jobPath, jsonData["jobID"])
		// Set directory of meta.json
		metaPath := filepath.Join(workDir, "meta.json")
		// Convert meta.json (in memory / loaded in) into an actual json object
		data, err := json.Marshal(jsonData)
		if err != nil {
			http.Error(w, "Server error attempting to create meta data", http.StatusInternalServerError)
			return
		}
		// Write the meta.json file into local disk
		err = os.WriteFile(metaPath, data, 0644)
		if err != nil {
			http.Error(w, "Server error attempting to write meta data", http.StatusInternalServerError)
			return
		}
		// Upload updated meta.json file
		err = r2Client.UploadMetadata(ctx, metaPath, jobID)
		if err != nil {
			http.Error(w, "Unable to upload meta data", http.StatusInternalServerError)
			return
		}
	} else {
		// Reuse CRID to avoid unnecessary resource creation
		crid = jsonData["view_CRID"]
	}
	// Construct share command
	shareCmd := exec.Command("qurl", "share", crid)
	// Capture output and error
	qurlLinkBytes, err := shareCmd.CombinedOutput()
	if err != nil {
		http.Error(w, "Server error attempting to share resource", http.StatusUnprocessableEntity)
		return
	}
	// Set header info 
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	// Set response with qURL link to open
	response := map[string]string{
		"status": "success",
		"qurl_link": strings.TrimSpace(string(qurlLinkBytes)),
	}
	json.NewEncoder(w).Encode(response)
}

func downloadHandler(w http.ResponseWriter, r *http.Request) {
	// Ensure proper http method before beginning
	if r.Method != http.MethodGet {
		http.Error(w, "use GET", http.StatusMethodNotAllowed)
		return
	}
	// Set context
	ctx, _ := context.WithTimeout(r.Context(), 30*time.Second)
	// Set up R2 client to begin upload
	r2Client, err := storage.NewR2Client(ctx)
	if err != nil {
		// Set error if command construction fails
		http.Error(w, "Server error attempting to connect to Cloudflare R2", http.StatusInternalServerError)
		return
	}
	// Extract job ID from request
	jobID := r.PathValue("jobID")
	// Fetch the job ID's respective meta.json file
	jsonData, err := r2Client.GetMetadata(ctx, jobID)
	if err != nil {
		http.Error(w, "Server error attempting to get meta data", http.StatusInternalServerError)
		return
	}
	// Set crid variable
	var crid string
	// Check if there is already a CRID to share
	if jsonData["download_CRID"] == "" {
		// Extract download URL from meta.json
		downloadURL := jsonData["downloadURL"]
		// Construct publish command
		publishCmd := exec.Command("qurl", "publish", "-q", downloadURL)
		// Capture output and error
		cridBytes, err := publishCmd.CombinedOutput()
		if err != nil {
			http.Error(w, "Server error attempting to publish resource", http.StatusInternalServerError)
			return
		}
		// Extract crid
		crid = strings.TrimSpace(string(cridBytes))
		// Set crid value
		jsonData["download_CRID"] = crid
		// Construct job id direcotry
		workDir := filepath.Join(jobPath, jsonData["jobID"])
		// Set meta.json path
		metaPath := filepath.Join(workDir, "meta.json")
		// Convert meta.json (in memory / loaded in) into an actual json object
		data, err := json.Marshal(jsonData)
		if err != nil {
			http.Error(w, "Server error attempting to create meta data", http.StatusInternalServerError)
			return
		}
		// Write json file into local disk
		err = os.WriteFile(metaPath, data, 0644)
		if err != nil {
			http.Error(w, "Server error attempting to write meta data", http.StatusInternalServerError)
			return
		}
		// Upload updated meta.json file
		err = r2Client.UploadMetadata(ctx, metaPath, jobID)
		if err != nil {
			http.Error(w, "Unable to upload meta data", http.StatusInternalServerError)
			return
		}
	} else {
		// Reuse CRID to avoid to avou unnecessary resource creation
		crid = jsonData["download_CRID"]
	}
	// Construct share command
	shareCmd := exec.Command("qurl", "share", crid)
	// Capture output and error
	qurlLinkBytes, err := shareCmd.CombinedOutput()
	if err != nil {
		http.Error(w, "Server error attempting to share resource", http.StatusUnprocessableEntity)
		return
	}
	// Set header info
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	// Set response with qURL link to open
	response := map[string]string{
		"status": "success",
		"qurl_link": strings.TrimSpace(string(qurlLinkBytes)),
	}
	json.NewEncoder(w).Encode(response)
}

func sweepStaleJobs(root string, maxAge time.Duration, now time.Time) (removed int, err error) {
	// Read job_store directory
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, err
	}
	// Iterate over job id directories
	for _, e := range entries {
		// Ensure we are only iterating over jobs
		if !e.IsDir() {
			// job_store should only ever contain per-job subdirectories;
			// skip anything else rather than deleting it.
			continue
		}
		// Extract job id directory info
		info, err := e.Info()
		if err != nil {
			log.Printf("sweeper: could not stat %s: %v", e.Name(), err)
			continue
		}
		// Check if it passes the max age that a job id directory should live to
		if now.Sub(info.ModTime()) > maxAge {
			// Set path directory of job id
			path := filepath.Join(root, e.Name())
			// Ensure directory removal is sucessful
			if err := os.RemoveAll(path); err != nil {
				log.Printf("sweeper: failed to remove %s: %v", path, err)
				continue
			}
			// Increment job removed count
			removed++
		}
	}
	// Return count and no errors
	return removed, nil
}

func startJobStoreSweeper(root string, interval, maxAge time.Duration) {
	// Set up ticker using provided interval
	ticker := time.NewTicker(interval)
	go func() {
		// Iterate over read-only channcel from interval
		for range ticker.C {
			// Sweep jobs
			removed, err := sweepStaleJobs(root, maxAge, time.Now())
			if err != nil {
				log.Printf("sweeper: error reading job store %s: %v", root, err)
				continue
			}
			// Log jobs removed (if any)
			if removed > 0 {
				log.Printf("sweeper: removed %d stale job(s) older than %s", removed, maxAge)
			}
		}
	}()
}

func deleteCRID(crid string) error {
	// Ensure crid isn't empty before beginnign
	if crid == "" {
		return nil
	}
	// Construct deletion command
	cmd := exec.Command("qurl", "delete", crid)
	// Capture output and error
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("Deleting %s: %s: %w",  crid, strings.TrimSpace(string(output)), err)
	}
	// Return nothing (success)
	return nil
}

func sweepR2Jobs(ctx context.Context, r2Client *storage.R2Client, maxAge time.Duration, now time.Time) (removed int, err error) {
	// Extract a list of all job ids in the Cloudflare R2 storage
	jobIDs, err := r2Client.ListJobPrefixes(ctx)
	if err != nil {
		return 0, err
	}
	// Iterate over each job
	for _, jobID := range jobIDs {
		// Extract meta data from each job id directory
		meta, err := r2Client.GetMetadata(ctx, jobID)
		if err != nil {
			log.Printf("r2 sweeper: could not read meta.json for %s, skipping: %v", jobID, err)
			continue
		}
		// Extract the data when the view / download urls were created
		createdAtStr, ok := meta["createdAt"]
		if !ok || createdAtStr == "" {
			log.Printf("r2 sweeper: %s has no createdAt, skipping", jobID)
			continue
		}
		// Parse into an actual time object
		createdAt, err := time.Parse(time.RFC3339, createdAtStr)
		if err != nil {
			log.Printf("r2 sweeper: %s has unparseable createdAt %q, skipping", jobID, createdAtStr)
			continue
		}
		// Check if the given job id has "expired" (should be 1 day)
		if now.Sub(createdAt) <= maxAge {
			continue
		}
		// Ensure successful deletion of view CRID
		if err := deleteCRID(meta["view_CRID"]); err != nil {
			log.Printf("r2 sweeper: %s view CRID revoke failed: %v", jobID, err)
		}
		// Ensure successful deletion of download CRID
		if err := deleteCRID(meta["download_CRID"]); err != nil {
			log.Printf("r2 sweeper: %s download CRID revoke failed: %v", jobID, err)
		}
		// Ensure deletion of job id directory in Cloudflare R2 storage
		if err := r2Client.DeleteJobPrefix(ctx, jobID); err != nil {
			log.Printf("r2 sweeper: failed to delete %s: %v", jobID, err)
			continue
		}
		// Increase sweep count
		removed++
	}
	// Return sweep count and no errors
	return removed, nil
}

func startR2Sweeper(interval, maxAge time.Duration) {
	// Set ticker from given time interval
	ticker := time.NewTicker(interval)
	go func() {
		// Iterate over ticker time read-only channcel interval
		for range ticker.C {
			// Setup context
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			// Construct R2Client for sweeping
			r2Client, err := storage.NewR2Client(ctx)
			if err != nil {
				log.Printf("r2 sweeper: could not connect to R2: %v", err)
				cancel()
				continue
			}
			// Sweep job ids
			removed, err := sweepR2Jobs(ctx, r2Client, maxAge, time.Now())
			cancel()
			if err != nil {
				log.Printf("r2 sweeper: error listing jobs: %v", err)
				continue
			}
			// Log how many jobs were sweeped (if any)
			if removed > 0 {
				log.Printf("r2 sweeper: removed %d stale job(s) older than %s", removed, maxAge)
			}
		}
	}()
}

func main() {
	// Load in .env variables from either a local .env or env variables set on Render. (pretty sure this is useless)
	if err := godotenv.Load(); err != nil {
		fmt.Printf("warning: could not load .env variables: %v\n", err)
	}
	// Ensure the persistent job store exists when the server starts
	if err := os.MkdirAll(jobPath, 0o755); err != nil {
		log.Fatalf("Failed to create job store directory: %v", err)
	}
	// Set up server mux
	mux := http.NewServeMux()
	mux.HandleFunc("/convert", convertHandler)
	mux.HandleFunc("GET /jobs/{jobID}/view", viewHandler)
	mux.HandleFunc("GET /jobs/{jobID}/download", downloadHandler)
	// Serve the front-end (index.html, etc.) from ./static
	mux.Handle("/", http.FileServer(http.Dir("./static")))

	// Start up sweepers (local and cloud)
	startJobStoreSweeper(jobPath, 4*time.Hour, 24*time.Hour)
	startR2Sweeper(24*time.Hour, 24*time.Hour)
 
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

