// Command loadgen sends synthetic events to a goodwill server at a steady
// rate, to check throughput and memory use.
//
// It needs a site's ingest key, so that each event can carry its own made-up
// visitor address. Addresses are drawn from the IPv6 documentation range.
//
//	go run ./scripts/loadgen -url http://127.0.0.1:8080 -website <id> -key <key> -rate 100 -duration 60s
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	base := flag.String("url", "http://127.0.0.1:8080", "public address of the server")
	website := flag.String("website", "", "website ID")
	key := flag.String("key", "", "ingest key for the site")
	rate := flag.Int("rate", 100, "events per second")
	duration := flag.Duration("duration", 30*time.Second, "how long to run")
	visitors := flag.Int("visitors", 5000, "number of distinct made-up visitors")
	flag.Parse()
	if *website == "" || *key == "" {
		fmt.Fprintln(os.Stderr, "loadgen: -website and -key are required")
		os.Exit(2)
	}

	paths := []string{"/", "/pricing", "/docs", "/docs/install", "/blog", "/blog/first-post", "/about", "/contact"}
	refs := []string{"", "", "", "https://www.google.com/", "https://duckduckgo.com/", "https://news.ycombinator.com/", "https://example.org/links"}
	agents := []string{
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15",
		"Mozilla/5.0 (X11; Linux x86_64; rv:142.0) Gecko/20100101 Firefox/142.0",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1",
	}
	screens := []string{"390x844", "1280x720", "1440x900", "1920x1080", "820x1180"}

	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: 64}}
	var ok, failed atomic.Int64
	var slowest atomic.Int64
	var wg sync.WaitGroup

	send := func() {
		defer wg.Done()
		v := rand.IntN(*visitors)
		payload := map[string]any{
			"website": *website, "hostname": "example.com", "url": paths[rand.IntN(len(paths))],
			"referrer": refs[rand.IntN(len(refs))], "screen": screens[v%len(screens)], "language": "en-GB",
			"title": "Load test", "ip": fmt.Sprintf("2001:db8:%x:%x::1", v>>16, v&0xffff), "userAgent": agents[v%len(agents)],
		}
		if rand.IntN(5) == 0 {
			payload["name"] = "signup"
			payload["data"] = map[string]any{"plan": []string{"free", "pro", "team"}[rand.IntN(3)], "seats": rand.IntN(10) + 1}
		}
		body, _ := json.Marshal(map[string]any{"type": "event", "payload": payload})
		req, _ := http.NewRequest("POST", *base+"/api/send", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+*key)
		start := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			failed.Add(1)
			return
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if d := time.Since(start).Microseconds(); d > slowest.Load() {
			slowest.Store(d)
		}
		if resp.StatusCode == http.StatusOK {
			ok.Add(1)
		} else {
			failed.Add(1)
		}
	}

	tick := time.NewTicker(time.Second / time.Duration(*rate))
	defer tick.Stop()
	deadline := time.After(*duration)
	started := time.Now()
loop:
	for {
		select {
		case <-tick.C:
			wg.Add(1)
			go send()
		case <-deadline:
			break loop
		}
	}
	wg.Wait()
	elapsed := time.Since(started).Seconds()
	fmt.Printf("sent %d events in %.1fs (%.0f/s), %d failed, slowest response %.1f ms\n",
		ok.Load()+failed.Load(), elapsed, float64(ok.Load()+failed.Load())/elapsed, failed.Load(), float64(slowest.Load())/1000)
	if failed.Load() > 0 {
		os.Exit(1)
	}
}
