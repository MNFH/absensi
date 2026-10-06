package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/absensi/internal/attendance"
	"github.com/absensi/internal/bot"
	"github.com/absensi/internal/config"
	"github.com/absensi/internal/db"
	"github.com/absensi/internal/geocode"
	"github.com/absensi/internal/nlu"
	"github.com/absensi/internal/sheets"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()

	store, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	for _, id := range cfg.InitialAdmins {
		if err := store.EnsureAdminByID(ctx, id); err != nil {
			log.Fatalf("seed admin %d: %v", id, err)
		}
	}

	if err := bot.SeedHolidays(ctx, store); err != nil {
		log.Fatalf("seed holidays: %v", err)
	}
	for _, p := range cfg.InitialAdminPhones {
		if err := store.EnsureAdminByPhone(ctx, p); err != nil {
			log.Fatalf("seed admin %s: %v", p, err)
		}
	}

	sh, err := sheets.New(ctx, cfg.CredentialsFile, cfg.SpreadsheetID, cfg.Location)
	if err != nil {
		log.Fatalf("sheets: %v", err)
	}
	att := attendance.NewService(sh, cfg.Location).WithBackdate(cfg.MaxBackdateDays)

	var ai *nlu.Client
	if cfg.GeminiAPIKey != "" {
		ai = nlu.New(cfg.GeminiAPIKey, cfg.GeminiModel)
		log.Printf("natural-language chat enabled (Gemini model %s)", cfg.GeminiModel)
	} else {
		log.Println("GEMINI_API_KEY not set: only /datang, /pulang and the keywords datang/pulang work")
	}

	h, err := bot.New(cfg.BotToken, store, att, cfg.Location, cfg.MaxBackdateDays, ai, geocode.New(cfg.GeocodeAgent))
	if err != nil {
		log.Fatalf("bot: %v", err)
	}
	go h.SyncAllCommands(ctx)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() { <-sig; log.Println("shutting down"); h.Stop() }()

	log.Printf("bot started as @%s", h.Username())
	h.Start()
}
