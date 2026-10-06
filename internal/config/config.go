package config

import (
	"fmt"
	"github.com/absensi/internal/db"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	BotToken           string
	DatabaseURL        string
	CredentialsFile    string
	SpreadsheetID      string
	Location           *time.Location
	InitialAdmins      []int64
	InitialAdminPhones []string // normalised
	MaxBackdateDays    int

	GeminiAPIKey string // optional; empty disables natural-language chat
	GeminiModel  string
	GeocodeAgent string // User-Agent for Nominatim (their policy requires one)
}

func Load() (*Config, error) {
	c := &Config{
		BotToken:        os.Getenv("BOT_TOKEN"),
		DatabaseURL:     os.Getenv("DATABASE_URL"),
		CredentialsFile: os.Getenv("GOOGLE_CREDENTIALS_FILE"),
		SpreadsheetID:   os.Getenv("SPREADSHEET_ID"),
		MaxBackdateDays: 62, // two calendar months: people forget, HR reminds late
		GeminiAPIKey:    os.Getenv("GEMINI_API_KEY"),
		GeminiModel:     os.Getenv("GEMINI_MODEL"),
		GeocodeAgent:    os.Getenv("GEOCODE_USER_AGENT"),
	}
	if c.GeminiModel == "" {
		c.GeminiModel = "gemini-3.1-flash-lite,gemini-3.5-flash,gemini-flash-lite-latest"
	}
	if c.GeocodeAgent == "" {
		c.GeocodeAgent = "absensi-bot/1.0"
	}
	for name, v := range map[string]string{
		"BOT_TOKEN": c.BotToken, "DATABASE_URL": c.DatabaseURL,
		"GOOGLE_CREDENTIALS_FILE": c.CredentialsFile, "SPREADSHEET_ID": c.SpreadsheetID,
	} {
		if v == "" {
			return nil, fmt.Errorf("env %s is required", name)
		}
	}

	tz := os.Getenv("TIMEZONE")
	if tz == "" {
		tz = "Asia/Jakarta"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, fmt.Errorf("TIMEZONE: %w", err)
	}
	c.Location = loc

	for _, p := range strings.Split(os.Getenv("INITIAL_ADMIN_IDS"), ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		id, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("INITIAL_ADMIN_IDS: %q is not a number", p)
		}
		c.InitialAdmins = append(c.InitialAdmins, id)
	}
	for _, p := range strings.Split(os.Getenv("INITIAL_ADMIN_PHONES"), ",") {
		if strings.TrimSpace(p) == "" {
			continue
		}
		n, err := db.NormalizePhone(p)
		if err != nil {
			return nil, fmt.Errorf("INITIAL_ADMIN_PHONES: %q: %w", p, err)
		}
		c.InitialAdminPhones = append(c.InitialAdminPhones, n)
	}
	if len(c.InitialAdmins) == 0 && len(c.InitialAdminPhones) == 0 {
		return nil, fmt.Errorf("set INITIAL_ADMIN_PHONES or INITIAL_ADMIN_IDS (at least one HR admin)")
	}

	if v := os.Getenv("MAX_BACKDATE_DAYS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("MAX_BACKDATE_DAYS invalid")
		}
		c.MaxBackdateDays = n
	}
	return c, nil
}
