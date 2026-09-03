package storage

import (
	"context"
	"fmt"
	"os"
	"strings"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"encoding/json"
)

type R2Client struct {
	client     *s3.Client
	bucket     string
	publicBase string
}

func NewR2Client(ctx context.Context) (*R2Client, error) {
	accountID := os.Getenv("R2_ACCOUNT_ID")
	accessKey := os.Getenv("R2_ACCESS_KEY_ID")
	secretKey := os.Getenv("R2_SECRET_ACCESS_KEY")
	bucket := os.Getenv("R2_BUCKET")
	publicBase := os.Getenv("R2_PUBLIC_BASE_URL")

	if accountID == "" || accessKey == "" || secretKey == "" || bucket == "" || publicBase == "" {
		return nil, fmt.Errorf("missing required R2 environment variables")
	}

	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion("auto"),
		config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("loading base AWS config: %w", err)
	}

	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(fmt.Sprintf("https://%s.r2.cloudflarestorage.com", accountID))
	})

	return &R2Client{client: client, bucket: bucket, publicBase: publicBase}, nil
}

var extToContentType = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".webp": "image/webp",
	".bmp":  "image/bmp",
	".gif":  "image/gif",
}

func contentTypeFor(ext string) string {
	if ct, ok := extToContentType[ext]; ok {
		return ct
	}
	return "application/octet-stream" // forces download instead of inline render — treat as a bug if you hit this
}

// Uploads converted output into Cloudflare R2 object and returns download link
func (r *R2Client) UploadJobImage(ctx context.Context, jobID, localPath, ext string) (donwloadKey string, downloadURL string, err error) {
	f, err := os.Open(localPath)
	if err != nil {
		return "", "", fmt.Errorf("opening local file: %w", err)
	}
	defer f.Close()
	key := fmt.Sprintf("%s/%s%s", jobID, jobID, ext)
	_, err = r.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(r.bucket),
		Key:         aws.String(key),
		Body:        f,
		ContentType: aws.String(contentTypeFor(ext)),
		ContentDisposition: aws.String(fmt.Sprintf("attachment; filename=\"%s%s\"", jobID, ext)),
	})
	if err != nil {
		return "", "", fmt.Errorf("uploading to R2: %w", err)
	}
	url := fmt.Sprintf("%s/%s", r.publicBase, key)
	return key, url, nil
}

func (r *R2Client) CreateViewCopy(ctx context.Context, downloadKey, ext string) (viewKey string, viewURL string, err error) {
	// "job-xyz/job-xyz.png" -> "job-xyz/job-xyz-view.png"
	base := strings.TrimSuffix(downloadKey, ext)
	viewKey = base + "-view" + ext
	copySource := fmt.Sprintf("%s/%s", r.bucket, downloadKey)
	_, err = r.client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:            aws.String(r.bucket),
		Key:               aws.String(viewKey),
		CopySource:        aws.String(copySource),
		ContentType:       aws.String(contentTypeFor(ext)),
		MetadataDirective: types.MetadataDirectiveReplace,
	})
	if err != nil {
		return "", "", fmt.Errorf("copying view object in R2: %w", err)
	}
	viewURL = fmt.Sprintf("%s/%s", r.publicBase, viewKey)
	return viewKey, viewURL, nil
}

func (r *R2Client) UploadMetadata(ctx context.Context, localPath string, jobID string) (err error) {
	jobLocation := fmt.Sprintf("%s/meta.json", jobID)
	f, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("opening meta.json file: %w", err)
	}
	defer f.Close()
	_, err = r.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(r.bucket),
		Key:         aws.String(jobLocation),
		Body:        f,
		ContentType: aws.String("application/json"),
	})
	if err != nil {
		return fmt.Errorf("uploading to R2: %w", err)
	}
	return nil
}

func (r *R2Client) DeleteJobImage(ctx context.Context, jobID, ext string) (err error) {
	key := fmt.Sprintf("%s/%s%s", jobID, jobID, ext)
	_, err = r.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(r.bucket),
		Key:    aws.String(key),
	})
	return err
}

func (r *R2Client) GetMetadata(ctx context.Context, jobID string) (data map[string]string, err error) {
	key := fmt.Sprintf("%s/meta.json", jobID)
	metaData, err := r.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(r.bucket),
		Key: aws.String(key),
	})
	if err != nil {
		return nil, err
	}
	defer metaData.Body.Close()
	var jsonData map[string]string
	err = json.NewDecoder(metaData.Body).Decode(&jsonData)
	if err != nil {
		return nil, err
	}
	return jsonData, nil
}

func main() {
	return;
}

