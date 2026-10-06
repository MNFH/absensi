# Loads .env into the environment and starts the bot.
Set-Location $PSScriptRoot
if (-not (Test-Path .env)) { Write-Error ".env not found (copy .env.example)"; exit 1 }
Get-Content .env | ForEach-Object {
    $line = $_.Trim()
    if ($line -eq '' -or $line.StartsWith('#')) { return }
    $k, $v = $line -split '=', 2
    Set-Item -Path "env:$($k.Trim())" -Value $v.Trim()
}
go run ./cmd/bot
