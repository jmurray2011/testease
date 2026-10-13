$ErrorActionPreference = 'Stop'

function Fact([string] $Key, [string] $Value) {
    Add-Content -LiteralPath $env:TESTEASE_FACTS -Value ("fact`t$Key`t$Value")
}

function No-Reading([string] $Key, [string] $Reason) {
    Add-Content -LiteralPath $env:TESTEASE_FACTS -Value ("no-reading`t$Key`t$Reason")
}

Fact 'example.value' 'replace-me'
Write-Output 'probe report goes to stdout'
exit 0
