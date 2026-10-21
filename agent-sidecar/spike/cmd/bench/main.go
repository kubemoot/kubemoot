// Command bench measures request latency to one URL: N sequential (or C concurrent) calls, reporting
// p50, p95 and p99. Streaming mode also reports the time to the first byte.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	url := flag.String("url", "http://127.0.0.1:8081/v1/chat/completions", "URL to POST to")
	n := flag.Int("n", 1000, "calls")
	c := flag.Int("c", 1, "concurrency")
	warm := flag.Int("warmup", 100, "warmup calls, not counted")
	stream := flag.Bool("stream", false, "stream the reply")
	flag.Parse()
	body := fmt.Sprintf(`{"model":"default","stream":%v,"messages":[{"role":"user","content":"hi"}]}`, *stream)
	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 256}}
	var bad atomic.Int64
	one := func() (time.Duration, time.Duration) {
		t, f, ok := call(client, *url, body)
		if !ok {
			bad.Add(1)
		}
		return t, f
	}
	for i := 0; i < *warm; i++ {
		one()
	}
	total, first := make([]time.Duration, 0, *n), make([]time.Duration, 0, *n)
	var mu sync.Mutex
	var wg sync.WaitGroup
	jobs := make(chan struct{})
	for w := 0; w < *c; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range jobs {
				t, f := one()
				mu.Lock()
				total, first = append(total, t), append(first, f)
				mu.Unlock()
			}
		}()
	}
	for i := 0; i < *n; i++ {
		jobs <- struct{}{}
	}
	close(jobs)
	wg.Wait()
	fmt.Printf("non-200 replies: %d of %d\n", bad.Load(), *n+*warm)
	report("total", total)
	if *stream {
		report("first-byte", first)
	}
}

func call(client *http.Client, url, body string) (time.Duration, time.Duration, bool) {
	start := time.Now()
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return time.Since(start), 0, false
	}
	first := time.Since(start)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return time.Since(start), first, resp.StatusCode == http.StatusOK
}

func report(name string, d []time.Duration) {
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	q := func(p float64) float64 { return float64(d[int(float64(len(d)-1)*p)].Microseconds()) / 1000 }
	fmt.Printf("%-11s n=%d p50=%.3fms p95=%.3fms p99=%.3fms max=%.3fms\n", name, len(d), q(.5), q(.95), q(.99), q(1))
}
