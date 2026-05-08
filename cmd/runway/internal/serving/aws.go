package serving

import (
	"context"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

// AWSS3Uploader implements S3Uploader using the AWS SDK v2 S3 client.
type AWSS3Uploader struct {
	Client *s3.Client
}

func (u *AWSS3Uploader) PutObject(ctx context.Context, bucket, key string, body io.Reader) error {
	_, err := u.Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   body,
	})
	return err
}

// AWSSSMWriter implements SSMWriter using the AWS SDK v2 SSM client.
type AWSSSMWriter struct {
	Client *ssm.Client
}

func (w *AWSSSMWriter) PutParameter(ctx context.Context, name, value string) error {
	_, err := w.Client.PutParameter(ctx, &ssm.PutParameterInput{
		Name:      aws.String(name),
		Value:     aws.String(value),
		Type:      ssmtypes.ParameterTypeString,
		Overwrite: aws.Bool(true),
	})
	return err
}
