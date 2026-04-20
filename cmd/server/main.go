package main

import (
	"log"
	"net/http"

	"github.com/shortontech/codex-claude-bridge/internal/config"
	"github.com/shortontech/codex-claude-bridge/internal/server"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	srv := server.New(cfg)
	addr := ":" + cfg.Port
	log.Printf("listening on %s", addr)
	if err := http.ListenAndServe(addr, srv.Routes()); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
