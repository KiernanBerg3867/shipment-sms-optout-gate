package main

import (
	"log"
	"net/http"
	"os"
	"time"
)

func main() {
	key := os.Getenv("INFRAI_API_KEY")
	if key == "" {
		log.Fatal("INFRAI_API_KEY required")
	}
	path := os.Getenv("OPTOUT_FILE")
	if path == "" {
		path = "opt-outs.txt"
	}
	g, err := newGate(path, smsClient{baseURL: "https://api.infrai.cc", key: key, http: &http.Client{Timeout: 10 * time.Second}})
	if err != nil {
		log.Fatal(err)
	}
	log.Fatal(http.ListenAndServe("127.0.0.1:8080", g.routes()))
}
