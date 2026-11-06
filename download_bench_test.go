package main

import (
	"bytes"
	"context"
	"fmt"
	"hash/crc32"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// fakeStore is an HTTP object store with tunable latency and bandwidth, so download
// performance can be measured repeatably without a real network.
type fakeStore struct {
	data      []byte
	ttfb      time.Duration // wait before every response
	connSetup time.Duration // extra wait on a connection's first request, standing in for TCP and TLS set-up
	rate      float64       // bytes per second per response; 0 for no limit
	conns     int64         // connections opened, read with atomic
}

type connKey struct{}

func (s *fakeStore) start() *httptest.Server {
	srv := httptest.NewUnstartedServer(s)
	// ConnContext runs in the accept loop, so it must not sleep: connSetup is paid in the handler.
	srv.Config.ConnContext = func(ctx context.Context, _ net.Conn) context.Context {
		atomic.AddInt64(&s.conns, 1)
		return context.WithValue(ctx, connKey{}, new(bool))
	}
	srv.Start()
	return srv
}

func (s *fakeStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// A connection serves one request at a time, so this flag needs no lock.
	if used := r.Context().Value(connKey{}).(*bool); !*used {
		*used = true
		time.Sleep(s.connSetup)
	}
	time.Sleep(s.ttfb)
	w.Header().Set("Content-Type", "application/octet-stream")
	if s.rate > 0 {
		w = &pacedWriter{ResponseWriter: w, rate: s.rate, start: time.Now()}
	}
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(s.data))
}

// pacedWriter holds a response body to rate bytes per second.
type pacedWriter struct {
	http.ResponseWriter
	rate  float64
	start time.Time
	sent  int64
}

func (p *pacedWriter) Write(b []byte) (int, error) {
	n, err := p.ResponseWriter.Write(b)
	p.sent += int64(n)
	time.Sleep(time.Until(p.start.Add(time.Duration(float64(p.sent) / p.rate * float64(time.Second)))))
	return n, err
}

// The s3like store approximates an S3 GET from EC2: the rate is about the per-connection speed
// we measured; the latencies are guesses.
const (
	s3likeFirstByteLatency = 20 * time.Millisecond
	s3likeConnSetup        = 20 * time.Millisecond
	s3likeRate             = 90e6 // bytes per second per connection
)

// benchChunkSize is small enough that every worker in a benchmark downloads several chunks.
const benchChunkSize = 4 << 20

// useTestOpts sets the global options the download path reads, until tb ends.
func useTestOpts(tb testing.TB, chunkSize int64) {
	saved, savedFailPercent := opts, testReadFailPercent
	tb.Cleanup(func() {
		opts, testReadFailPercent = saved, savedFailPercent
		log.SetOutput(os.Stderr)
	})
	opts.ChunkSize = chunkSize // HttpDownloader only reports range support for files larger than this
	opts.RetryCount = 1
	testReadFailPercent = 0 // injected failures would make every chunk a series of new requests
	log.SetOutput(io.Discard)
}

// download fetches url through fastar's HTTP downloader, as the CLI does, and returns the data's CRC-32.
func download(url string, chunkSize int64, numWorkers int) (uint32, error) {
	downloader := GetDownloader(url, false, false, numWorkers)
	// Each call is a fresh client, like a fresh process; close its connections so they don't pile up.
	defer downloader.(HttpDownloader).client.CloseIdleConnections()
	h := crc32.NewIEEE()
	_, err := io.Copy(h, GetDownloadStream(downloader, chunkSize, numWorkers))
	return h.Sum32(), err
}

// The benchmark is only as good as the store, so check it honours ranges, delays and rate.
func TestFakeStore(t *testing.T) {
	data := []byte(RandomString(1000))
	s := &fakeStore{data: data, ttfb: 30 * time.Millisecond, connSetup: 50 * time.Millisecond, rate: 10e3}
	transfer := func(n int) time.Duration { return time.Duration(float64(n) / s.rate * float64(time.Second)) }
	srv := s.start()
	defer srv.Close()

	get := func(rangeHeader string) ([]byte, time.Duration) {
		req, _ := http.NewRequest("GET", srv.URL, nil)
		if rangeHeader != "" {
			req.Header.Set("Range", rangeHeader)
		}
		begin := time.Now()
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return body, time.Since(begin)
	}

	// Only lower bounds: sleeps guarantee them, while upper bounds would be flaky on a busy machine.
	body, took := get("")
	if !bytes.Equal(body, data) {
		t.Fatal("full GET: wrong body")
	}
	if want := s.connSetup + s.ttfb + transfer(len(data)); took < want {
		t.Fatalf("full GET took %v, want at least %v (connSetup + ttfb + transfer)", took, want)
	}

	const rangeStart, rangeEnd = 100, 200
	body, took = get(fmt.Sprintf("bytes=%d-%d", rangeStart, rangeEnd-1))
	if !bytes.Equal(body, data[rangeStart:rangeEnd]) {
		t.Fatalf("range GET: got %d bytes, want data[%d:%d]", len(body), rangeStart, rangeEnd)
	}
	if want := s.ttfb + transfer(rangeEnd-rangeStart); took < want {
		t.Fatalf("range GET took %v, want at least %v (ttfb + transfer)", took, want)
	}
	if n := atomic.LoadInt64(&s.conns); n != 1 {
		t.Fatalf("opened %d connections, want 1: the second request should reuse the first's", n)
	}
}

// End to end over HTTP, which the other download tests don't cover.
func TestDownloadOverHTTP(t *testing.T) {
	data := []byte(RandomString(10000))
	want := crc32.ChecksumIEEE(data)
	srv := (&fakeStore{data: data}).start()
	defer srv.Close()

	// 3333 doesn't divide the size; 20000 is larger than the file, so fastar falls back to one GET.
	for _, chunkSize := range []int64{1000, 3333, 20000} {
		for _, workers := range []int{1, 3, 8} {
			t.Run(fmt.Sprintf("chunk=%d/workers=%d", chunkSize, workers), func(t *testing.T) {
				useTestOpts(t, chunkSize)
				if got, err := download(srv.URL, chunkSize, workers); err != nil || got != want {
					t.Fatalf("got CRC %08x, error %v; want CRC %08x", got, err, want)
				}
			})
		}
	}
}

// Each worker downloads one chunk at a time, so connections should stay near one per worker;
// each extra one pays TCP and TLS set-up again. Go opens a few more when a request starts
// dialling just before another connection comes free. Without reuse, 4 workers opened 11-31.
func TestDownloadReusesConnections(t *testing.T) {
	const chunkSize, chunks, maxConnsPerWorker = 1000, 64, 2
	data := []byte(RandomString(chunkSize * chunks))
	want := crc32.ChecksumIEEE(data)
	for _, workers := range []int{1, 4, 8} {
		t.Run(fmt.Sprintf("workers=%d", workers), func(t *testing.T) {
			useTestOpts(t, chunkSize)
			store := &fakeStore{data: data}
			srv := store.start()
			defer srv.Close()
			if got, err := download(srv.URL, chunkSize, workers); err != nil || got != want {
				t.Fatalf("got CRC %08x, error %v; want CRC %08x", got, err, want)
			}
			if n, limit := atomic.LoadInt64(&store.conns), int64(maxConnsPerWorker*workers); n > limit {
				t.Errorf("opened %d connections for %d chunks, want at most %d", n, chunks, limit)
			}
		})
	}
}

// BenchmarkDownload measures download throughput (MB/s) and connections opened per download.
// Compare branches with: go test -run '^$' -bench Download -count 10 | tee new.txt; benchstat old.txt new.txt
func BenchmarkDownload(b *testing.B) {
	data := make([]byte, 32*benchChunkSize)
	rand.New(rand.NewSource(1)).Read(data)
	want := crc32.ChecksumIEEE(data)

	stores := []struct {
		name  string
		store *fakeStore
	}{
		// No latency or bandwidth limit: measures fastar's own CPU and memory cost.
		{"local", &fakeStore{data: data}},
		{"s3like", &fakeStore{data: data, ttfb: s3likeFirstByteLatency, connSetup: s3likeConnSetup, rate: s3likeRate}},
	}
	for _, s := range stores {
		srv := s.store.start()
		defer srv.Close()
		for _, workers := range []int{1, 4, 8} {
			b.Run(fmt.Sprintf("%s/workers=%d", s.name, workers), func(b *testing.B) {
				useTestOpts(b, benchChunkSize)
				b.SetBytes(int64(len(data)))
				before := atomic.LoadInt64(&s.store.conns)
				for i := 0; i < b.N; i++ {
					if got, err := download(srv.URL, benchChunkSize, workers); err != nil || got != want {
						b.Fatalf("got CRC %08x, error %v; want CRC %08x", got, err, want)
					}
				}
				b.ReportMetric(float64(atomic.LoadInt64(&s.store.conns)-before)/float64(b.N), "conns/op")
			})
		}
	}
}
