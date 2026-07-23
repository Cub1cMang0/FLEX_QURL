# flex-web-api
## Prerequisites

- Go 1.21+ (`go version` to check)
- `flex-convert-cli` binary built and placed in this same directory
  (or update `cliPath` in main.cpp to point elsewhere)

## Run it

```bash
go build -o flex-web-api main.go
./flex-web-api
# listening on :8080
```

## Test it

```bash
curl -X POST "http://localhost:8080/convert?to=ico" \
  -F "file=@file_name_here.png" \
  -o result.ico
```

## How it works

1. Accepts a multipart file upload + a `to` query param for the target format
2. Writes the upload to a per-request temp directory (auto-cleaned after
   the response is sent)
3. Shells out to `flex-convert-cli <input> <output_dir> <output_ext>` --
   this service never touches image bytes itself, it only orchestrates
4. Streams the converted file back as the HTTP response