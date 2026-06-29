package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"
)

type order struct {
	ID       string `json:"id"`
	Customer string `json:"customer"`
	Region   string `json:"region"`
	Total    int    `json:"total"`
	Status   string `json:"status"`
}

type summary struct {
	OrderCount        int     `json:"orderCount"`
	Revenue           int     `json:"revenue"`
	AverageOrderValue float64 `json:"averageOrderValue"`
	BusiestRegion     string  `json:"busiestRegion"`
}

var orders = []order{
	{ID: "ord_1001", Customer: "Northstar Goods", Region: "west", Total: 184, Status: "packing"},
	{ID: "ord_1002", Customer: "Copperline Supply", Region: "central", Total: 96, Status: "queued"},
	{ID: "ord_1003", Customer: "Harbor House", Region: "east", Total: 242, Status: "shipped"},
	{ID: "ord_1004", Customer: "Trailhead Labs", Region: "west", Total: 133, Status: "packing"},
}

func main() {
	port := env("PORT", env("WORKYARD_PORT", "4103"))
	host := env("HOST", "0.0.0.0")

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"ok":      true,
			"service": "analytics",
			"port":    port,
			"time":    time.Now().UTC().Format(time.RFC3339),
		})
	})
	mux.HandleFunc("/summary", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, summarize())
	})
	mux.HandleFunc("/score", func(w http.ResponseWriter, r *http.Request) {
		region := r.URL.Query().Get("region")
		writeJSON(w, map[string]any{
			"region": region,
			"score":  80 + len(region)*3,
		})
	})

	addr := host + ":" + port
	log.Printf("analytics listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}

func summarize() summary {
	regions := map[string]int{}
	revenue := 0
	for _, order := range orders {
		revenue += order.Total
		regions[order.Region]++
	}
	busiest := "unknown"
	for region, count := range regions {
		if count > regions[busiest] {
			busiest = region
		}
	}
	average := 0.0
	if len(orders) > 0 {
		average = float64(revenue) / float64(len(orders))
	}
	return summary{
		OrderCount:        len(orders),
		Revenue:           revenue,
		AverageOrderValue: round2(average),
		BusiestRegion:     busiest,
	}
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("content-type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func round2(value float64) float64 {
	rounded, _ := strconv.ParseFloat(strconv.FormatFloat(value, 'f', 2, 64), 64)
	return rounded
}
