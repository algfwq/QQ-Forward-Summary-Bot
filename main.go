package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	configPath := flag.String("config", "config.json", "path to config file")
	flag.Parse()

	cfg, err := LoadConfig(*configPath)
	fallbackLogger := log.New(os.Stdout, "[qq-summary-bot] ", log.LstdFlags|log.Lmsgprefix)
	if err != nil {
		fallbackLogger.Fatalf("load config failed: %v", err)
	}

	logger, logCloser, err := SetupLogger(cfg.Log)
	if err != nil {
		fallbackLogger.Fatalf("setup logger failed: %v", err)
	}
	defer logCloser.Close()

	root, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	bot := NewBot(root, cfg, logger)

	mux := http.NewServeMux()
	mux.HandleFunc(cfg.Server.WSPath, bot.HandleReverseWebSocket)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		status := "disconnected"
		if bot.IsConnected() {
			status = "connected"
		}
		_, _ = fmt.Fprintf(w, "qq-summary-bot is running, reverse websocket endpoint: %s, napcat: %s\n", cfg.Server.WSPath, status)
	})

	server := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           mux,
		ReadHeaderTimeout: time.Duration(cfg.Server.ReadHeaderTimeoutSeconds) * time.Second,
	}

	go func() {
		<-root.Done()
		_ = bot.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()

	logger.Printf("listening on %s, reverse websocket path %s", cfg.Server.Listen, cfg.Server.WSPath)
	logger.Printf("logging to %s", cfg.Log.Dir)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Fatalf("server error: %v", err)
	}
}
