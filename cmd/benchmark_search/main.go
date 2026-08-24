package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type result struct {
	URL        string  `json:"url"`
	Iterations int     `json:"iterations"`
	P50MS      float64 `json:"p50_ms"`
	P95MS      float64 `json:"p95_ms"`
	P99MS      float64 `json:"p99_ms"`
	MinMS      float64 `json:"min_ms"`
	MaxMS      float64 `json:"max_ms"`
	Status     int     `json:"status"`
}

func main() {
	url := flag.String("url", "http://localhost:8080/api/v1/references/search?type=user&type=post&type=thread&type=debate&type=feed&type=article&type=artist&type=album&type=song&type=playlist&type=podcast&type=episode&type=video&type=person&type=event&type=channel&type=collection&q=search", "full search URL")
	token := flag.String("token", "", "optional bearer token")
	iterations := flag.Int("iterations", 30, "measured requests")
	warmup := flag.Int("warmup", 5, "warmup requests")
	concurrency := flag.Int("concurrency", 1, "concurrent measured requests")
	flag.Parse()
	if *iterations < 1 {
		fatal(fmt.Errorf("iterations must be greater than zero"))
	}
	if *concurrency < 1 {
		fatal(fmt.Errorf("concurrency must be greater than zero"))
	}

	client := &http.Client{Timeout: 30 * time.Second}
	for i := 0; i < *warmup; i++ {
		if _, err := request(client, *url, *token); err != nil {
			fatal(err)
		}
	}

	samples := make([]float64, 0, *iterations)
	var statusValue atomic.Int32
	sampleChannel := make(chan float64, *iterations)
	errorChannel := make(chan error, 1)
	semaphore := make(chan struct{}, *concurrency)
	var workers sync.WaitGroup
	for i := 0; i < *iterations; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			select {
			case semaphore <- struct{}{}:
			case <-time.After(30 * time.Second):
				select {
				case errorChannel <- fmt.Errorf("timed out waiting for benchmark concurrency slot"):
				default:
				}
				return
			}
			defer func() { <-semaphore }()
			started := time.Now()
			code, err := request(client, *url, *token)
			if err != nil {
				select {
				case errorChannel <- err:
				default:
				}
				return
			}
			statusValue.Store(int32(code))
			sampleChannel <- float64(time.Since(started).Microseconds()) / 1000
		}()
	}
	workers.Wait()
	close(sampleChannel)
	select {
	case err := <-errorChannel:
		fatal(err)
	default:
	}
	for sample := range sampleChannel {
		samples = append(samples, sample)
	}
	if len(samples) == 0 {
		fatal(fmt.Errorf("benchmark produced no samples"))
	}
	status := int(statusValue.Load())
	sort.Float64s(samples)
	output := result{
		URL: *url, Iterations: len(samples), Status: status,
		MinMS: samples[0], MaxMS: samples[len(samples)-1],
		P50MS: percentile(samples, 0.50), P95MS: percentile(samples, 0.95), P99MS: percentile(samples, 0.99),
	}
	encoded, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		fatal(err)
	}
	fmt.Println(string(encoded))
}

func request(client *http.Client, url, token string) (int, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, err = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, err
}

func percentile(samples []float64, fraction float64) float64 {
	index := int(float64(len(samples)-1) * fraction)
	return samples[index]
}

func fatal(err error) {
	fmt.Printf("benchmark failed: %v\n", err)
	os.Exit(1)
}
