# Absensi — Telegram Attendance Bot

A Telegram bot for office attendance. Employees check in (**datang**) and check out (**pulang**) by chatting with the bot. Every record has the exact time, WFO/WFH status and location. HR manages who may use the bot and gets recaps as Excel files.

- **Users and roles** are stored in **PostgreSQL**.
- **Attendance records** are stored only in **Google Sheets**, one tab per month (`2026-10`, `2026-11`, …).
- **Free-text chat** ("saya sudah sampai kantor", "rekap bulan september") is understood with **Google Gemini**.
- The bot's messages and commands are in Indonesian.

---

## Contents
1. [Features](#features)
2. [Tech specs](#tech-specs)
3. [Setup](#setup)
4. [Configuration reference](#configuration-reference)
5. [Running](#running)
6. [Using the bot](#using-the-bot)
7. [Testing](#testing)
8. [Demo data](#demo-data)
9. [Deploying to a server (VPS)](#deploying-to-a-server-vps)
10. [Maintenance](#maintenance)
11. [Security, privacy and known limits](#security-privacy-and-known-limits)

---

## Features

**Employees**
- Check in and out by writing normally ("saya sudah datang", "baru nyampe, wfh", "pulang jam 5 sore") or with `/datang` and `/pulang`.
- The bot asks for WFO/WFH and for the current location. Location is required for check-in and optional for check-out. The address is looked up automatically.
- A forgotten time can be filled in later, up to 62 days back ("kemarin lupa absen pulang, jam 5 sore", `/pulang 5 Oktober 17.00`).
- `/status`, `/riwayat` and `/libur` show today's records, recent history and upcoming holidays.

**HR (admin)**
- Register employees by **phone number**, one at a time or by uploading an Excel/CSV file. Employees verify themselves by sharing their Telegram contact.
- Revoke or restore access, change roles, names and phone numbers. Every change is written to an audit log.
- `/summary` or a free-text request ("rekap fajar bulan ini") returns an **Excel recap**: a people × dates matrix plus a detail sheet with times and locations.
- `/today` shows who is present, absent or still at work today.
- In office groups, `/ingatkan` tags members who haven't checked in or out, and `/jadwal` sets up **automatic reminders** (default 12:00 and 23:00 on working days).
- **Holidays**: the official Indonesian holidays and cuti bersama for 2026 and 2027 are built in. HR can add company holidays or turn a holiday into a working day.

---

## Tech specs

### Stack

| Part | Technology |
|---|---|
| Language | Go 1.27 (single static binary) |
| Telegram | [`gopkg.in/telebot.v3`](https://github.com/tucnak/telebot), long polling (no public URL or HTTPS needed) |
| Database | PostgreSQL 14+ via [`jackc/pgx/v5`](https://github.com/jackc/pgx). Migrations are embedded and run on startup |
| Attendance storage | Google Sheets API v4 (`google.golang.org/api/sheets/v4`), service-account auth |
| Natural language | Google Gemini REST API (`generateContent` with a JSON response schema), with model fallback |
| Reverse geocoding | OpenStreetMap Nominatim (free, no key) |
| Excel import/export | [`xuri/excelize/v2`](https://github.com/xuri/excelize) |
| Timezone | Configurable, default `Asia/Jakarta` |

### Architecture

```
 Telegram ──long polling──▶ cmd/bot ──▶ internal/bot (handlers, auth, groups, reminders, scheduler)
                                          │
        ┌──────────────┬──────────────────┼─────────────────┬──────────────┬─────────────────┐
        ▼              ▼                  ▼                 ▼              ▼                 ▼
   internal/db    internal/attendance  internal/nlu   internal/geocode  internal/report  internal/importer
   PostgreSQL     rules + pairing      Gemini         Nominatim         Excel recap      Excel/CSV users
   (users, audit, │
    reminders,    ▼
    holidays)  internal/sheets ──▶ Google Sheets (attendance, one tab per month)
```

### Project layout

```
cmd/bot/              main program
cmd/demoseed/         fills database + sheet with fictional demo data
internal/attendance/  check-in/out rules, sessions, summaries, periods
internal/bot/         Telegram handlers: worker flow, admin, groups, reminders, holidays, import
internal/config/      environment variables
internal/db/          PostgreSQL store + migrations/*.sql
internal/geocode/     Nominatim reverse geocoding
internal/holiday/     official Indonesian holidays (SKB 3 Menteri)
internal/idfmt/       Indonesian date/duration/phone formatting
internal/importer/    .xlsx/.csv user list parser
internal/nlu/         Gemini intent extraction
internal/report/      Excel recap builder
internal/sheets/      Google Sheets storage
internal/timeparse/   "5 Januari 17.00"-style time parsing
```

### Where data lives

**PostgreSQL** (users and settings, no attendance):

| Table | Contents |
|---|---|
| `users` | phone (normalised `62…`), Telegram ID (set when verified), name, role `admin`/`worker`, active flag |
| `audit_log` | every grant/revoke/role/phone/import/holiday change and every rejected access attempt |
| `reminder_groups` | per Telegram group: automatic reminder times and when they were last sent |
| `holidays` | official holidays (source `skb`) and HR changes (source `hr`), with an active flag |

**Google Sheets** (attendance only), one tab per month with these columns:

`Waktu Dicatat | Tanggal | Jam | Telegram ID | Nama | Telepon | Tipe | Status | Latitude | Longitude | Alamat | Peta | Sumber`

- *Waktu Dicatat* is when the bot received the message. *Tanggal* and *Jam* are the attendance time, which differs when a time was entered manually (`Sumber` = `manual`).
- *Tipe* is `DATANG` or `PULANG`, and *Status* is `WFO` or `WFH`.
- *Peta* is a Google Maps link.
- Don't reorder or delete columns by hand: the bot reads these tabs for validation and recaps. The bot keeps the last ~76 days in memory, loaded at startup, so **restart the bot after editing rows by hand**.

### How the main rules work
- **Identity**: HR registers a phone number. The employee taps "Bagikan nomor telepon saya"; the bot accepts it only if the contact belongs to the sender's own account and matches an active registered number. That Telegram account is then locked to the number, and only an admin can change it (`/unbind`, `/setphone`).
- **Check-in/out pairing** is validated against the person's history:
  - One check-in per day.
  - A new day's check-in is allowed even if yesterday's check-out was forgotten; the bot then asks for the missing time.
  - A check-out *without* a time only closes a check-in from the last 16 hours, which still allows night shifts.
  - Late entries up to `MAX_BACKDATE_DAYS` (default 62) are slotted into the past.
- **Time of a check-in/out without an explicit time** is when the message was *sent*, not when the bot processed it. Telegram queues messages while the bot is offline (up to ~24 h) and delivers them later. *Waktu Dicatat* still shows the processing time, so delays stay visible, and the user is told when their record used the earlier send time.
- **Gemini** only turns a message into `{action, status, time, period, person}`. The bot validates every field itself and does all access checks and writes. Admin changes are only possible through explicit commands, never through free text. The employee list is never sent to Gemini.

---

## Setup

You need: a Telegram account, a Google account, PostgreSQL, and Go 1.27+ (only to build).

### 1. Telegram bot
1. In Telegram, open **@BotFather** → `/newbot` → choose a name and a username ending in `bot`. Copy the **token**.
2. *(For use in groups)* `/setprivacy` → choose your bot → **Disable**. Otherwise group members must write commands as `/ingatkan@YourBot`. After changing this, remove the bot from the group and add it again.
3. Get your own numeric Telegram ID from **@userinfobot**. You'll use it as the first admin.

### 2. Google Sheets
1. In [Google Cloud Console](https://console.cloud.google.com/), create a project (or use an existing one).
2. **APIs & Services → Library → Google Sheets API → Enable.**
3. **IAM & Admin → Service Accounts → Create service account.** No roles are needed. Open it → **Keys → Add key → JSON** and save the file in the project folder as `service-account.json`.
4. Create an empty Google Spreadsheet and **share it as Editor** with the service account's email (`…@….iam.gserviceaccount.com`).
5. Copy the **spreadsheet ID** from its URL: `https://docs.google.com/spreadsheets/d/<THIS PART>/edit`.

### 3. Gemini (optional, recommended)
Create an API key at [Google AI Studio](https://aistudio.google.com/apikey). Without it, the bot still works with commands and with messages that start with "datang" or "pulang".

### 4. PostgreSQL
Any PostgreSQL 14+. Create a database and user:

```sql
CREATE ROLE absensi LOGIN PASSWORD 'choose-a-password';
CREATE DATABASE absensi OWNER absensi;
```

- **Windows:** `winget install PostgreSQL.PostgreSQL.16`, then run the SQL above with `psql -U postgres -h localhost`.
- **Ubuntu/Debian:** `sudo apt install postgresql`, then `sudo -u postgres psql` and run the SQL above.
- **Docker:** `docker compose up -d` starts PostgreSQL 16 with user, password and database `absensi`.

Tables are created automatically when the bot starts.

### 5. Configure
Copy `.env.example` to `.env` and fill it in (see the [reference](#configuration-reference)). At minimum:

```ini
BOT_TOKEN=123456:ABC...
DATABASE_URL=postgres://absensi:choose-a-password@localhost:5432/absensi?sslmode=disable
GOOGLE_CREDENTIALS_FILE=service-account.json
SPREADSHEET_ID=1AbC...
INITIAL_ADMIN_IDS=123456789
GEMINI_API_KEY=...
```

`.env` and `service-account.json` are git-ignored. **Never commit them or paste them into chats.**

### 6. Start and log in
Run the bot (next section). Then in Telegram, open your bot and send `/start`. Because your ID is in `INITIAL_ADMIN_IDS`, you are an admin right away. Register employees with `/adduser 08123456789 Budi Santoso` or `/import`.

---

## Configuration reference

| Variable | Required | Default | Meaning |
|---|---|---|---|
| `BOT_TOKEN` | yes | | Token from @BotFather |
| `DATABASE_URL` | yes | | PostgreSQL connection URL |
| `GOOGLE_CREDENTIALS_FILE` | yes | | Path to the service-account JSON key |
| `SPREADSHEET_ID` | yes | | ID of the attendance spreadsheet |
| `INITIAL_ADMIN_IDS` | one of these two | | Comma-separated Telegram IDs that are always active admins (usable immediately) |
| `INITIAL_ADMIN_PHONES` | one of these two | | Comma-separated phone numbers that are always active admins (they verify by sharing their contact) |
| `TIMEZONE` | no | `Asia/Jakarta` | Timezone for all dates and times |
| `MAX_BACKDATE_DAYS` | no | `62` | How far back a time may be entered manually |
| `GEMINI_API_KEY` | no | | Enables free-text chat |
| `GEMINI_MODEL` | no | `gemini-3.1-flash-lite,gemini-3.5-flash,gemini-flash-lite-latest` | Model, or a comma-separated fallback list tried in order when a model is busy, out of quota or retired |
| `GEOCODE_USER_AGENT` | no | `absensi-bot/1.0` | Sent to Nominatim. Their policy asks for an identifying value, e.g. `absensi-bot/1.0 (hr@company.com)` |

---

## Running

**Windows (PowerShell):** `.\run.ps1` loads `.env` and runs the bot.

**Linux/macOS:** `./run.sh` does the same.

**Build a binary:**

```bash
go build -o absensi-bot ./cmd/bot                          # this machine
GOOS=linux GOARCH=amd64 go build -o absensi-bot ./cmd/bot  # for a Linux server (arm64 for ARM)
```

On startup the log shows `bot started as @YourBot`. The bot creates or updates database tables, loads the official holidays and syncs each user's command menu.

---

## Using the bot

### Employees (private chat)

| How | What it does |
|---|---|
| "saya sudah datang di kantor", `/datang` | Check in; the bot asks WFO/WFH and for your location |
| "pulang dulu", `/pulang` | Check out; location optional |
| `/datang 5 Oktober 08.00 wfo`, "kemarin pulang jam 5 sore" | Check in or out with a past time (≤ 62 days) |
| `/status`, `/riwayat`, `/libur` | Today's records, last 10 records, upcoming holidays |
| `/batal` | Cancel a check-in/out that is waiting for status or location |

Times without AM/PM are read as 24-hour (`5.00` = 05:00, `17.00` = 17:00). `5.00PM` also works.

New employees: open the bot, send `/start`, tap **"Bagikan nomor telepon saya"**. The number must already be registered by HR.

### HR / admin (private chat)

| Command | What it does |
|---|---|
| `/adduser 08123456789 Budi Santoso` | Register an employee by phone |
| `/import` | Shows the format, then send an `.xlsx`/`.csv` file (header: `telepon`, `nama`, optional `role`). The bot shows a preview and applies nothing until you tap **Terapkan** |
| `/users` | All users: ✅ active, ⏳ not verified yet, ⛔ revoked |
| `/revoke <phone>` · `/grant <phone>` | Remove or restore access |
| `/setrole <phone> admin\|worker` · `/setname <phone> <name>` | Change role or name |
| `/setphone <old> <new>` · `/unbind <phone>` | New number, or a new Telegram account (re-verification needed) |
| `/today` | Who is present, absent or still working today |
| `/summary [week\|month\|YYYY-MM] [phone]` | Excel recap |
| "rekap bulan september", "rekap fajar minggu lalu", "laporan 1-15 september" | Same recap via free text (max 93 days) |
| `/libur tambah 2 Januari Libur kantor` | Add a company holiday (ranges: `29 Desember sampai 31 Desember …`) |
| `/libur hapus 24 Desember` | Make a holiday a normal working day (e.g. cuti bersama, office open) |
| `/audit [n]` | Recent changes and rejected access attempts |

Safety rules: an admin can't revoke or demote themselves, and the last active admin can't be removed.

**The Excel recap** has two sheets:
- **Rekap**: one row per person, one column per date. Cells are colour-coded: WFO, WFH, forgot check-out, working now, absent, weekend, holiday. Totals per person: days present, working days, WFO, WFH, absent, forgotten check-outs, total hours.
- **Detail**: one row per day per person, with times, duration, notes, addresses and map links.

Nobody is marked absent on holidays, before they were registered, or after they were revoked.

### In groups
The bot never records attendance or shows data in a group. Anyone who calls it there gets a button that opens the private chat. Admins can additionally use:

| Command | What it does |
|---|---|
| `/ingatkan` | Tag group members who haven't checked in today |
| `/ingatkan pulang` | Tag members who checked in today but not out |
| `/jadwal on` | Automatic reminders Mon–Fri: check-in 12:00, check-out 23:00 (skipped on holidays) |
| `/jadwal datang 13:00` · `/jadwal pulang off` · `/jadwal off` · `/jadwal` | Change, disable or show the schedule |

Only members of that group are tagged. Automatic reminders go out at most once per group per day, even across restarts, and post nothing when everyone has already checked in or out.

---

## Testing

```bash
go test ./...
```

Database integration tests need a **throwaway** database. The tests empty its tables, so don't point this at the real database:

```bash
TEST_DATABASE_URL=postgres://absensi:password@localhost:5432/absensi_test?sslmode=disable go test ./internal/db/
```

The tests cover:
- time parsing
- check-in/out rules (forgotten check-outs, late entries, night shifts)
- Gemini response validation and model fallback
- Excel import and recap contents
- holiday data checked against the official weekdays
- admin safety rules, phone binding and reminder bookkeeping in PostgreSQL

---

## Demo data

To show the bot to HR without real data. **Stop the bot first**, then with `.env` loaded:

```bash
go run ./cmd/demoseed          # replace demo data: 10 fictional employees, attendance from 1 Sep until now
go run ./cmd/demoseed -all     # also delete real accounts' attendance rows (accounts stay registered)
go run ./cmd/demoseed -clean   # remove all demo data
```

Options:
- `-office lat,lng` sets the office location for WFO records.
- `-from YYYY-MM-DD` sets the first day of generated data.

Demo users have phone numbers `0800-000-01xx` and negative Telegram IDs, so no real person can log in as one, and cleanup only touches demo rows.

---

## Deploying to a server (VPS)

Any small Linux VM works (1 GB RAM is plenty). No domain, open port or HTTPS is needed.

1. Install PostgreSQL and create the database (see [Setup](#4-postgresql)).
2. Copy the binary (built with `GOOS=linux`), `.env` and `service-account.json` to e.g. `/opt/absensi/`.
3. Create `/etc/systemd/system/absensi.service`:

   ```ini
   [Unit]
   Description=Absensi Telegram bot
   After=network-online.target postgresql.service
   Wants=network-online.target

   [Service]
   WorkingDirectory=/opt/absensi
   EnvironmentFile=/opt/absensi/.env
   ExecStart=/opt/absensi/absensi-bot
   Restart=always
   RestartSec=5
   User=absensi

   [Install]
   WantedBy=multi-user.target
   ```

4. Start it and follow the log:

   ```bash
   sudo systemctl enable --now absensi
   journalctl -u absensi -f
   ```

Only run **one** copy of the bot per token. Telegram delivers each update to a single poller.

Back up the database regularly, for example with a daily `pg_dump absensi > backup.sql`. Attendance itself is in Google Sheets, which keeps its own version history.

---

## Maintenance

- **New year's holidays:** when the SKB 3 Menteri for a new year is published (usually around September), add its dates to `internal/holiday/indonesia.go` and to the weekday check in `indonesia_test.go`. They are loaded at the next start. Until then, HR can add days with `/libur tambah`.
- **Gemini model retired or over quota:** adjust `GEMINI_MODEL`. The bot already falls back through the list automatically.
- **Rows edited by hand in the sheet:** restart the bot so it reloads its history.
- **Rotating secrets:**
  - Bot token: @BotFather → `/revoke`.
  - Gemini key: delete it and create a new one in AI Studio.
  - Service-account key: delete it and create a new one in Cloud Console.

  Update `.env` or the JSON file, then restart.

---

## Security, privacy and known limits

- **Locations can be faked.** Fake-GPS apps or a pin picked on the map produce a normal location message. The bot only rejects forwarded locations. WFO/WFH is declared by the employee, not checked against an office geofence.
- **The spreadsheet contains personal data** (names, phone numbers, home locations of WFH days). Share it only with people who need it.
- **Phone verification** relies on Telegram attaching the contact owner's ID to a shared contact. It's strong in practice, but not a cryptographic proof.
- **Nominatim's public server** is meant for light use (max. 1 request/second). That's fine for an office, but heavy use needs a self-hosted instance.
- **Holidays** only know the national SKB dates for 2026–2027 plus whatever HR adds. Regional holidays (e.g. Nyepi in Bali only) must be added by HR.
- **Gemini prompts** contain the user's message text and the current date. The employee list and attendance data are never sent.
