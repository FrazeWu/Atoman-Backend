package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
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
	flag.Parse()

	client := &http.Client{Timeout: 30 * time.Second}
	for i := 0; i < *warmup; i++ {
		if _, err := request(client, *url, *token); err != nil {
			fatal(err)
		}
	}

	samples := make([]float64, 0, *iterations)
	status := 0
	for i := 0; i < *iterations; i++ {
		started := time.Now()
		code, err := request(client, *url, *token)
		if err != nil {
			fatal(err)
		}
		status = code
		samples = append(samples, float64(time.Since(started).Microseconds())/1000)
	}
	if len(samples) == 0 {
		fatal(fmt.Errorf("iterations must be greater than zero"))
	}
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
