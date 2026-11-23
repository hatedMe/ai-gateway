param(
    [string]$Token = $env:OPENAI_API_KEY,
    [string]$Model = $env:OPENAI_MODEL
)

$env:OPENAI_BASE_URL = 'https://opencode.ai/zen/go/v1'
if (-not [string]::IsNullOrWhiteSpace($Token)) {
    $env:OPENAI_API_KEY = $Token
}
if (-not [string]::IsNullOrWhiteSpace($Model)) {
    $env:OPENAI_MODEL = $Model
}

Push-Location (Split-Path -Parent $PSScriptRoot)
try {
    go run ./cmd/server
} finally {
    Pop-Location
}
