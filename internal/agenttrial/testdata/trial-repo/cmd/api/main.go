package main

import (
	"log"
	"net/http"
	"os"

	"example.com/paygate/internal/httpapi"
	"example.com/paygate/internal/store"
)

func main() {
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}

	s := store.New()
	s.Put(store.Account{ID: "demo", Email: "demo@example.com"})

	h := &httpapi.Handler{Store: s}
	log.Printf("paygate listening on %s", addr)
	if err := http.ListenAndServe(addr, h.Routes()); err != nil {
		log.Fatal(err)
	}
}
