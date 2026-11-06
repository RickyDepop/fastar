package main

import (
	"context"
	"errors"
	"io"
	"log"
	"mime/multipart"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"golang.org/x/sys/unix"
)

type S3Downloader struct {
	Url    string
	client *s3.Client
}

func (s3Downloader S3Downloader) GetFileInfo() (int64, bool, bool) {
	bucket, key := getBucketAndKey(s3Downloader.Url)
	resp, err := s3Downloader.client.HeadObject(context.Background(), &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		// HEAD errors have no body, so S3's error code is lost; a GET fails the same way and
		// reports it, so getObject can exit with the right status.
		getResp := s3Downloader.getObject(nil)
		getResp.Body.Close()
		return *getResp.ContentLength, true, false
	}
	return *resp.ContentLength, true, false
}

func (s3Downloader S3Downloader) Get() io.ReadCloser {
	return s3Downloader.getObject(nil).Body
}

func (s3Downloader S3Downloader) GetRange(start, end int64) io.ReadCloser {
	rangeString := GenerateRangeString([][]int64{{start, end}})
	return s3Downloader.getObject(&rangeString).Body
}

// S3 doesn't support multipart range requests right now, so this will never be used
// for actual file download. Still here for when they eventually do support it though.
func (s3Downloader S3Downloader) GetRanges(ranges [][]int64) (*multipart.Reader, error) {
	return nil, errors.New("multipart range requests not supported by S3")
}

func (s3Downloader S3Downloader) getObject(rangeString *string) *s3.GetObjectOutput {
	bucket, key := getBucketAndKey(s3Downloader.Url)
	params := &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	}
	if rangeString != nil {
		params.Range = aws.String(*rangeString)
	}
	resp, err := s3Downloader.client.GetObject(context.Background(), params)
	if err != nil {
		if strings.Contains(err.Error(), "404") {
			log.Println("404, fast failing:", err.Error())
			os.Exit(int(unix.ENOENT))
		} else if strings.Contains(err.Error(), "SignatureDoesNotMatch") {
			log.Println("Failed to authenticate:", err.Error())
			os.Exit(int(unix.EACCES))
		} else if strings.Contains(err.Error(), "no VPC endpoint policy allows") {
			log.Println("Failed to reach bucket due to VPC endpoint misconfiguration:", err.Error())
			os.Exit(int(unix.EHOSTUNREACH))
		}
		log.Fatal("Unexpected error getting S3 object: ", err.Error())
	}
	return resp
}

func getBucketAndKey(url string) (string, string) {
	parts := strings.Split(strings.Replace(url, "s3://", "", 1), "/")
	bucket := parts[0]
	key := strings.Join(parts[1:], "/")
	return bucket, key
}
