package main

import (
	"goftpd/plugins/sitebot/internal/bot"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	configPath := flag.String("config", "./plugins/sitebot/etc/config.yml", "Config file path")
	flag.Parse()
	
	// Load config
	cfg, err := bot.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	
	// Create bot
	b := bot.NewBot(cfg)
	b.Debug = cfg.Debug
	
	// Start bot
	if err := b.Start(); err != nil {
		log.Fatalf("Failed to start bot: %v", err)
	}
	
	log.Println("GoSitebot running...")
	
	// Handle signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	
	<-sigChan
	log.Println("Shutting down...")
	
	b.Stop()
}
