#Requires -Version 5.1
<#
.SYNOPSIS
    aisphere-hub authn E2E flow verification (Windows PowerShell edition).

.DESCRIPTION
    Walks through the full OAuth code flow:
      1. Get login URL from /v1/authn/login-url
      2. (Manual) Open URL in browser, log in to Casdoor, copy code from callback URL
      3. Exchange code for tokens via /v1/authn/exchange
      4. Call /v1/authn/me with the access token
      5. Call /v1/authn/introspect to verify token validity
      6. (Optional) Refresh the token via /v1/authn/refresh
      7. (Optional) Revoke the token via /v1/authn/revoke
      8. Verify /v1/authn/me returns 401 after revoke

.PARAMETER Hub
    Hub base URL. Default: http://127.0.0.1:8000

.PARAMETER RedirectUri
    OAuth callback URL. Must match Casdoor application's redirect_uri config.
    Default: http://localhost:3000/callback

.PARAMETER Scope
    OAuth scope. Default: read

.PARAMETER State
    OAuth state parameter. Default: aisphere-hub

.PARAMETER Code
    Pre-supplied authorization code (skip manual step 2). Useful for CI.

.EXAMPLE
    .\authn_e2e.ps1
    Run interactively; will prompt for the code after you log in.

.EXAMPLE
    .\authn_e2e.ps1 -Hub http://127.0.0.1:8000 -RedirectUri http://localhost:3000/callback
    Override hub base URL and redirect URI.

.EXAMPLE
    .\authn_e2e.ps1 -Code "abc123" -State "aisphere-hub"
    Non-interactive: skip step 2 prompt.

.NOTES
    Prerequisites:
      - PowerShell 5.1+ (Windows 10/11 ships with 5.1; install PS 7 for better JSON)
      - Hub running on $Hub
      - Casdoor running and reachable from hub AND from your browser
      - Casdoor application configured with redirect_uri = $RedirectUri
#>

[CmdletBinding()]
param(
    [string]$Hub         = 'http://127.0.0.1:8000',
    [string]$RedirectUri = 'http://localhost:3000/callback',
    [string]$Scope       = 'read',
    [string]$State       = 'aisphere-hub',
    [string]$Code        = ''
)

$ErrorActionPreference = 'Stop'

# --- Helpers -----------------------------------------------------------------

function Write-Step  { param([string]$Msg) Write-Host "`n[STEP] $Msg" -ForegroundColor Cyan }
function Write-OK    { param([string]$Msg) Write-Host "[OK]   $Msg" -ForegroundColor Green }
function Write-Warn2 { param([string]$Msg) Write-Host "[WARN] $Msg" -ForegroundColor Yellow }
function Write-Err2  { param([string]$Msg) Write-Host "[ERR]  $Msg" -ForegroundColor Red }
function Write-Dim   { param([string]$Msg) Write-Host "       $Msg" -ForegroundColor DarkGray }

function Pretty-Json {
    param([string]$Json)
    try {
        $obj = $Json | ConvertFrom-Json
        $obj | ConvertTo-Json -Depth 10
    } catch {
        Write-Host $Json
    }
}

function Truncate-Token {
    param([string]$Token, [int]$Len = 40)
    if ([string]::IsNullOrEmpty($Token)) { return '' }
    if ($Token.Length -le $Len) { return $Token }
    return $Token.Substring(0, $Len) + '...'
}

# --- Config echo -------------------------------------------------------------

Write-Host '=================================================' -ForegroundColor DarkGray
Write-Host ' aisphere-hub authn E2E flow (PowerShell)' -ForegroundColor White
Write-Host '=================================================' -ForegroundColor DarkGray
Write-Dim "Hub:          $Hub"
Write-Dim "Redirect URI: $RedirectUri"
Write-Dim "Scope:        $Scope"
Write-Dim "State:        $State"

# --- Step 1: GET /v1/authn/login-url -----------------------------------------

Write-Step '1/8  GET /v1/authn/login-url'

$query = @{
    redirect_uri = $RedirectUri
    scope        = $Scope
    state        = $State
} | ForEach-Object {
    $i = [IEnumerator]::new(@($query.Keys))
} # placeholder; we'll build query manually below

# Build query string manually to ensure correct URL encoding
$queryStr = "redirect_uri=$([uri]::EscapeDataString($RedirectUri))&scope=$([uri]::EscapeDataString($Scope))&state=$([uri]::EscapeDataString($State))"
$url = "$Hub/v1/authn/login-url?$queryStr"
Write-Dim "GET $url"

try {
    $loginResp = Invoke-RestMethod -Uri $url -Method Get -ContentType 'application/json'
} catch {
    Write-Err2 "Request failed: $_"
    Write-Err2 "Response body: $($_.ErrorDetails.Message)"
    exit 1
}

Write-OK 'Response:'
Pretty-Json ($loginResp | ConvertTo-Json -Depth 5)

$loginUrl = $loginResp.login_url
if ([string]::IsNullOrEmpty($loginUrl)) {
    Write-Err2 'No login_url in response'
    exit 1
}

# --- Step 2: Manual browser login --------------------------------------------

if ([string]::IsNullOrEmpty($Code)) {
    Write-Step '2/8  MANUAL — Open this URL in a browser and log in to Casdoor:'
    Write-Host ''
    Write-Host "  $loginUrl" -ForegroundColor Yellow
    Write-Host ''
    Write-Warn2 "After logging in, Casdoor will redirect to:"
    Write-Warn2 "  $RedirectUri`?code=XXXX&state=$State"
    Write-Warn2 'Copy the code query parameter from the redirected URL.'
    Write-Host ''
    $Code = Read-Host 'Paste code here'
    if ([string]::IsNullOrEmpty($Code)) {
        Write-Err2 'No code provided, aborting.'
        exit 1
    }
} else {
    Write-Step '2/8  Code supplied via -Code parameter; skipping manual login.'
    Write-Dim "Code: $Code"
}

# --- Step 3: POST /v1/authn/exchange -----------------------------------------

Write-Step '3/8  POST /v1/authn/exchange'

$exchangeBody = @{
    code         = $Code
    redirect_uri = $RedirectUri
    state        = $State
} | ConvertTo-Json -Compress

Write-Dim 'Request body:'
Write-Dim "  $exchangeBody"

try {
    $exchangeResp = Invoke-RestMethod -Uri "$Hub/v1/authn/exchange" -Method Post -ContentType 'application/json' -Body $exchangeBody
} catch {
    Write-Err2 "Exchange failed: $_"
    Write-Err2 "Response body: $($_.ErrorDetails.Message)"
    exit 1
}

Write-OK 'Response:'
Pretty-Json ($exchangeResp | ConvertTo-Json -Depth 5)

$accessToken  = $exchangeResp.access_token
$refreshToken = $exchangeResp.refresh_token
$idToken      = $exchangeResp.id_token
$expiresIn    = $exchangeResp.expires_in

if ([string]::IsNullOrEmpty($accessToken)) {
    Write-Err2 'Exchange failed: no access_token in response'
    exit 1
}

Write-Host ''
Write-Dim "Extracted:"
Write-Dim "  access_token:  $(Truncate-Token $accessToken)"
Write-Dim "  refresh_token: $(Truncate-Token $refreshToken)"
Write-Dim "  id_token:      $(Truncate-Token $idToken)"
Write-Dim "  expires_in:    ${expiresIn}s"

# --- Step 4: GET /v1/authn/me ------------------------------------------------

Write-Step '4/8  GET /v1/authn/me (with Bearer token)'

$headers = @{ Authorization = "Bearer $accessToken" }
Write-Dim "Authorization: Bearer $(Truncate-Token $accessToken)"

try {
    $meResp = Invoke-RestMethod -Uri "$Hub/v1/authn/me" -Method Get -Headers $headers
} catch {
    Write-Err2 "/me failed: $_"
    Write-Err2 "Response body: $($_.ErrorDetails.Message)"
    exit 1
}

Write-OK 'Response:'
Pretty-Json ($meResp | ConvertTo-Json -Depth 10)

# --- Step 5: POST /v1/authn/introspect ---------------------------------------

Write-Step '5/8  POST /v1/authn/introspect'

$introspectBody = @{
    token      = $accessToken
    token_type = 'access_token'
} | ConvertTo-Json -Compress

Write-Dim 'Request body:'
Write-Dim "  {`"token`": `"<access_token>`", `"token_type`": `"access_token`"}"

try {
    $introspectResp = Invoke-RestMethod -Uri "$Hub/v1/authn/introspect" -Method Post -ContentType 'application/json' -Body $introspectBody
} catch {
    Write-Err2 "/introspect failed: $_"
    Write-Err2 "Response body: $($_.ErrorDetails.Message)"
    exit 1
}

Write-OK 'Response:'
Pretty-Json ($introspectResp | ConvertTo-Json -Depth 10)

if (-not $introspectResp.active) {
    Write-Warn2 'Introspect returned active=false — token not valid?'
}

# --- Step 6: POST /v1/authn/refresh (optional) -------------------------------

Write-Step '6/8  POST /v1/authn/refresh (optional)'
Write-Warn2 'Press Enter to refresh the token, or Ctrl+C to skip...'
try { Read-Host } catch { exit 0 }

$refreshBody = @{ refresh_token = $refreshToken } | ConvertTo-Json -Compress
Write-Dim 'Request body:'
Write-Dim "  {`"refresh_token`": `"<refresh_token>`"}"

try {
    $refreshResp = Invoke-RestMethod -Uri "$Hub/v1/authn/refresh" -Method Post -ContentType 'application/json' -Body $refreshBody
} catch {
    Write-Err2 "/refresh failed: $_"
    Write-Err2 "Response body: $($_.ErrorDetails.Message)"
    exit 1
}

Write-OK 'Response:'
Pretty-Json ($refreshResp | ConvertTo-Json -Depth 5)

$newAccessToken = $refreshResp.access_token
if (-not [string]::IsNullOrEmpty($newAccessToken)) {
    Write-Dim "New access token: $(Truncate-Token $newAccessToken)"
    $accessToken = $newAccessToken
}

# --- Step 7: POST /v1/authn/revoke (optional) --------------------------------

Write-Step '7/8  POST /v1/authn/revoke (optional)'
Write-Warn2 'Press Enter to revoke the token and verify rejection, or Ctrl+C to skip...'
try { Read-Host } catch { exit 0 }

$revokeBody = @{
    token      = $accessToken
    token_type = 'access_token'
} | ConvertTo-Json -Compress

Write-Dim 'Request body:'
Write-Dim "  {`"token`": `"<access_token>`", `"token_type`": `"access_token`"}"

try {
    $revokeResp = Invoke-RestMethod -Uri "$Hub/v1/authn/revoke" -Method Post -ContentType 'application/json' -Body $revokeBody
} catch {
    Write-Err2 "/revoke failed: $_"
    Write-Err2 "Response body: $($_.ErrorDetails.Message)"
    exit 1
}

Write-OK 'Response:'
Pretty-Json ($revokeResp | ConvertTo-Json -Depth 5)

# --- Step 8: GET /v1/authn/me again (should 401) -----------------------------

Write-Step '8/8  GET /v1/authn/me again (should return 401 after revoke)'

$headers = @{ Authorization = "Bearer $accessToken" }
Write-Dim "Authorization: Bearer $(Truncate-Token $accessToken)"

$meStatus = 0
try {
    $null = Invoke-RestMethod -Uri "$Hub/v1/authn/me" -Method Get -Headers $headers
    $meStatus = 200
} catch [System.Net.WebException] {
    if ($_.Exception.Response) {
        $meStatus = [int]$_.Exception.Response.StatusCode
    } else {
        $meStatus = -1
    }
} catch {
    # PowerShell 7+: HttpResponseMessage is in $_.Response
    if ($_.Response) {
        $meStatus = [int]$_.Response.StatusCode
    } else {
        $meStatus = -1
    }
}

switch ($meStatus) {
    401 { Write-OK "Revocation works: /me returned 401 after revoke" }
    200 { Write-Err2 'Revocation FAILED: /me still returned 200 after revoke'
          Write-Err2 '  Check hub/internal/data/authn.go:RevokeLocal calls CachedTokenService.Invalidate.' }
    default { Write-Warn2 "Unexpected status: $meStatus" }
}

Write-Host ''
Write-Host '=================================================' -ForegroundColor Green
Write-Host ' E2E flow complete.' -ForegroundColor Green
Write-Host '=================================================' -ForegroundColor Green
