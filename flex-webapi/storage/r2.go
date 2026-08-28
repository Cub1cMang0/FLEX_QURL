package storage

import (
	"context"
	"fmt"
	"os"
	"github.com/joho/godotenv"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
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

func (r *R2Client) UploadJobImage(ctx context.Context, jobID, localPath, ext string) (string, error) {
	f, err := os.Open(localPath)
	if err != nil {
		return "", fmt.Errorf("opening local file: %w", err)
	}
	defer f.Close()

	key := fmt.Sprintf("%s/%s%s", jobID, jobID, ext)

	_, err = r.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(r.bucket),
		Key:         aws.String(key),
		Body:        f,
		ContentType: aws.String(contentTypeFor(ext)),
	})
	if err != nil {
		return "", fmt.Errorf("uploading to R2: %w", err)
	}

	return fmt.Sprintf("%s/%s", r.publicBase, key), nil
}

func (r *R2Client) DeleteJobImage(ctx context.Context, jobID, ext string) error {
	key := fmt.Sprintf("%s/%s%s", jobID, jobID, ext)
	_, err := r.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(r.bucket),
		Key:    aws.String(key),
	})
	return err
}

func main() {
	if err := godotenv.Load(); err != nil {
		fmt.Printf("warning: could not load .env: %v\n", err)
	}

	ctx := context.Background()

	r2Client, err := NewR2Client(ctx)
	if err != nil {
		fmt.Printf("failed to create R2 client: %v\n", err)
		return
	}

	result, er := r2Client.UploadJobImage(
		ctx,
		"job-123",
		"job-123/job-123.png",
		".png",
	)
	if err != nil {
		fmt.Printf("failed to delete image: %v\n", er)
		return
	}

	fmt.Printf("uploaded image %s: ", result)
}

