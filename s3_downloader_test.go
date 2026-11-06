package main

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// fileInfoFromFakeS3 calls GetFileInfo against a server that answers HEAD with headStatus and
// GET with 200, and returns the size and the request methods it saw.
func fileInfoFromFakeS3(t *testing.T, size, headStatus int) (int64, []string) {
	var mu sync.Mutex
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		methods = append(methods, r.Method)
		mu.Unlock()
		w.Header().Set("Content-Length", strconv.Itoa(size))
		if r.Method == http.MethodHead {
			w.WriteHeader(headStatus)
		}
	}))
	defer srv.Close()

	client := s3.New(s3.Options{
		Region:       "us-east-1",
		BaseEndpoint: aws.String(srv.URL),
		UsePathStyle: true,
		Credentials:  aws.AnonymousCredentials{},
		HTTPClient:   srv.Client(),
	})
	got, supportsRange, _ := S3Downloader{"s3://bucket/model.tar", client}.GetFileInfo()
	if !supportsRange {
		t.Error("GetFileInfo reported no range support")
	}
	return got, methods
}

// The size comes from a HEAD: a GET would make S3 start sending the whole object, and
// closing it early throws the connection away.
func TestS3FileInfoUsesHead(t *testing.T) {
	const size = 18834360320
	got, methods := fileInfoFromFakeS3(t, size, http.StatusOK)
	if got != size || len(methods) != 1 || methods[0] != http.MethodHead {
		t.Errorf("size %d from requests %v, want %d from one HEAD", got, methods, int64(size))
	}
}

// A failed HEAD falls back to a GET, whose error body getObject needs to choose the exit status.
func TestS3FileInfoFallsBackToGet(t *testing.T) {
	const size = 1000
	got, methods := fileInfoFromFakeS3(t, size, http.StatusForbidden)
	want := []string{http.MethodHead, http.MethodGet}
	if got != size || len(methods) != 2 || methods[0] != want[0] || methods[1] != want[1] {
		t.Errorf("size %d from requests %v, want %d from %v", got, methods, size, want)
	}
}
