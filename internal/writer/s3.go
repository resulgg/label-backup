package writer

import (
	"context"
	"fmt"
	"io"

	// "log" // Standard log replaced by Zap

	"label-backup/internal/logger" // Added for Zap logger
	"label-backup/internal/model"

	"go.uber.org/zap" // Added for Zap fields

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	// For Options.UsePathStyle
)

// countingReader wraps an io.Reader and counts the bytes read.
type countingReader struct {
	reader io.Reader
	count  int64
}

func (cr *countingReader) Read(p []byte) (n int, err error) {
	n, err = cr.reader.Read(p)
	cr.count += int64(n)
	return
}

func (cr *countingReader) BytesRead() int64 {
	return cr.count
}

const S3WriterType = "remote"

// S3Writer implements BackupWriter for saving to AWS S3.
type S3Writer struct {
	uploader   *manager.Uploader
	s3Client   *s3.Client // Keep client for other potential S3 ops, though uploader uses its own.
	bucketName string
	awsRegion  string
}

func init() {
	RegisterWriterFactory(S3WriterType, NewS3Writer)
}

// NewS3Writer creates a new S3Writer.
// It expects the S3 bucket name from globalConfig (e.g., BUCKET_NAME).
// AWS credentials and region are expected to be configured via standard AWS SDK mechanisms
// (env vars, shared credentials file, IAM roles), unless an ENDPOINT is provided
// or ACCESS_KEY_ID and SECRET_ACCESS_KEY are explicitly set in the environment.
func NewS3Writer(spec model.BackupSpec, globalConfig map[string]string) (BackupWriter, error) {
	bucket, ok := globalConfig[GlobalConfigKeyS3Bucket]
	if !ok || bucket == "" {
		logger.Log.Error("S3 bucket name not provided in global config", zap.String("key", GlobalConfigKeyS3Bucket))
		return nil, fmt.Errorf("S3 bucket name not provided in global config under key '%s'", GlobalConfigKeyS3Bucket)
	}

	region := globalConfig[GlobalConfigKeyS3Region] // Can be empty, SDK will use env var or profile
	s3Endpoint := globalConfig[GlobalConfigKeyS3Endpoint]
	accessKeyID := globalConfig[GlobalConfigKeyS3AccessKeyID]
	secretAccessKey := globalConfig[GlobalConfigKeyS3SecretAccessKey]

	var cfgLoadOptions []func(*awsconfig.LoadOptions) error
	cfgLoadOptions = append(cfgLoadOptions, awsconfig.WithRegion(region))

	if accessKeyID != "" && secretAccessKey != "" {
		logger.Log.Info("Using static S3 credentials from environment variables")
		staticCreds := credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, "") // Session token is empty
		cfgLoadOptions = append(cfgLoadOptions, awsconfig.WithCredentialsProvider(staticCreds))
	} else {
		logger.Log.Info("Static S3 credentials (ACCESS_KEY_ID, SECRET_ACCESS_KEY) not fully provided, using default AWS credential chain.")
	}

	if s3Endpoint != "" {
		logger.Log.Info("Custom S3 endpoint provided, configuring for S3-compatible service",
			zap.String("endpoint", s3Endpoint),
		)
		// For S3-compatible services, region might be ignored or can be a dummy value.
		// The endpoint resolver and path-style addressing are key.
		customResolver := aws.EndpointResolverWithOptionsFunc(func(service, r string, options ...interface{}) (aws.Endpoint, error) {
			if service == s3.ServiceID {
				return aws.Endpoint{
					URL:           s3Endpoint,
					SigningRegion: region, // Use provided region or SDK default
				}, nil
			}
			// Fallback to default resolution for other services
			return aws.Endpoint{}, &aws.EndpointNotFoundError{}
		})
		cfgLoadOptions = append(cfgLoadOptions, awsconfig.WithEndpointResolverWithOptions(customResolver))
	}

	cfg, err := awsconfig.LoadDefaultConfig(context.Background(), cfgLoadOptions...)
	if err != nil {
		logger.Log.Error("Failed to load AWS SDK config for S3Writer", zap.Error(err))
		return nil, fmt.Errorf("failed to load AWS SDK config: %w", err)
	}

	s3ClientOpts := []func(*s3.Options){
		func(o *s3.Options) {
			if s3Endpoint != "" {
				o.UsePathStyle = true
				logger.Log.Info("S3 client configured with UsePathStyle=true for custom endpoint.")
			}
		},
	}

	s3Client := s3.NewFromConfig(cfg, s3ClientOpts...)
	uploader := manager.NewUploader(s3Client)

	logger.Log.Info("S3Writer initialized", zap.String("bucket", bucket), zap.String("region", cfg.Region), zap.String("endpoint", s3Endpoint))
	return &S3Writer{
		uploader:   uploader,
		s3Client:   s3Client,
		bucketName: bucket,
		awsRegion:  cfg.Region,
	}, nil
}

// Type returns the type of the writer.
func (s3w *S3Writer) Type() string {
	return S3WriterType
}

// Write uploads the backup data from the reader to an S3 bucket.
func (s3w *S3Writer) Write(ctx context.Context, objectName string, reader io.Reader) (destination string, bytesWritten int64, err error) {
	logger.Log.Info("Uploading backup to S3",
		zap.String("bucket", s3w.bucketName),
		zap.String("key", objectName),
	)

	countingReader := &countingReader{reader: reader}

	result, err := s3w.uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s3w.bucketName),
		Key:    aws.String(objectName),
		Body:   countingReader, // Use the counting reader
	})
	if err != nil {
		logger.Log.Error("Failed to upload backup to S3",
			zap.String("bucket", s3w.bucketName),
			zap.String("key", objectName),
			zap.Error(err),
		)
		return "", 0, fmt.Errorf("failed to upload backup to S3 (bucket: %s, key: %s): %w", s3w.bucketName, objectName, err)
	}

	bytesWritten = countingReader.BytesRead()
	logger.Log.Info("Successfully uploaded backup to S3",
		zap.String("location", result.Location),
		zap.Int64("bytesWritten", bytesWritten),
	)
	return result.Location, bytesWritten, nil
}

// ListObjects lists backup objects from the S3 bucket, matching the given prefix.
func (s3w *S3Writer) ListObjects(ctx context.Context, prefix string) ([]BackupObjectMeta, error) {
	var objects []BackupObjectMeta
	logger.Log.Info("S3Writer: Listing objects", 
		zap.String("bucket", s3w.bucketName), 
		zap.String("prefix", prefix),
	)

	paginator := s3.NewListObjectsV2Paginator(s3w.s3Client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s3w.bucketName),
		Prefix: aws.String(prefix),
	})

	for paginator.HasMorePages() {
		if ctx.Err() != nil {
		    logger.Log.Warn("S3 listing cancelled or timed out", 
		        zap.String("bucket", s3w.bucketName), 
		        zap.String("prefix", prefix), 
		        zap.Error(ctx.Err()),
		    )
		    return nil, ctx.Err()
		}
		page, err := paginator.NextPage(ctx)
		if err != nil {
			logger.Log.Error("Failed to list S3 objects page", 
			    zap.String("bucket", s3w.bucketName), 
			    zap.String("prefix", prefix), 
			    zap.Error(err),
			)
			return nil, fmt.Errorf("failed to list S3 objects for bucket %s, prefix %s: %w", s3w.bucketName, prefix, err)
		}
		for _, obj := range page.Contents {
			var size int64
			if obj.Size != nil {
				size = *obj.Size
			}
			objects = append(objects, BackupObjectMeta{
				Key:          aws.ToString(obj.Key),
				LastModified: aws.ToTime(obj.LastModified),
				Size:         size,
			})
		}
	}

	logger.Log.Info("S3Writer: Found objects", 
	    zap.Int("count", len(objects)), 
	    zap.String("bucket", s3w.bucketName), 
	    zap.String("prefix", prefix),
	)
	return objects, nil
}

// DeleteObject deletes an object from the S3 bucket.
func (s3w *S3Writer) DeleteObject(ctx context.Context, key string) error {
	logger.Log.Info("S3Writer: Attempting to delete S3 object", 
	    zap.String("bucket", s3w.bucketName), 
	    zap.String("key", key),
	)

	_, err := s3w.s3Client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s3w.bucketName),
		Key:    aws.String(key),
	})

	if err != nil {
		// S3 does not typically error if the object doesn't exist, it just completes.
		// However, if there's an actual access error or other issue, it will be reported.
		logger.Log.Error("Failed to delete S3 object",
		    zap.String("bucket", s3w.bucketName),
		    zap.String("key", key),
		    zap.Error(err),
		)
		return fmt.Errorf("failed to delete S3 object (bucket: %s, key: %s): %w", s3w.bucketName, key, err)
	}

	logger.Log.Info("Successfully submitted deletion for S3 object", 
	    zap.String("bucket", s3w.bucketName), 
	    zap.String("key", key),
	)
	return nil
} 