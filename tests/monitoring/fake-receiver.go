// This fake exists only for the disposable Docker monitoring smoke test.
package main

import (
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
)

func main() {
	var writes, scrapes atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		scrapes.Add(1)
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintln(w, "metric_disk_stat_success 1")
	})
	mux.HandleFunc("/write", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		writes.Add(1)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/counts", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintf(w, "%d %d\n", writes.Load(), scrapes.Load()) })
	go func() { _ = http.ListenAndServe(":9101", mux) }()
	_ = http.ListenAndServe(":9201", mux)
}
