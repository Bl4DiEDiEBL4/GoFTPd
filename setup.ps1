param(
    [ValidateSet("install", "build", "configs", "certs")]
    [string]$Mode = "install",

    [ValidateSet("master", "slave", "both")]
    [string]$Config = "both",

    [switch]$SkipBuild,
    [switch]$SkipCerts,
    [switch]$Force,

    [string]$GoVersion = "1.25.0"
)

$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $MyInvocation.MyCommand.Path
$ToolsDir = Join-Path $Root ".tools"
Set-Location $Root

function Say($Message = "") {
    Write-Host $Message
}

function To-ConfigPath([string]$Path) {
    return ($Path -replace "\\", "/")
}

function Ensure-Dir([string]$Path) {
    if (-not (Test-Path -LiteralPath $Path)) {
        New-Item -ItemType Directory -Path $Path | Out-Null
    }
}

function Get-GoArch {
    if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64" -or $env:PROCESSOR_ARCHITEW6432 -eq "ARM64") {
        return "arm64"
    }
    return "amd64"
}

function Ensure-Go {
    $cmd = Get-Command go -ErrorAction SilentlyContinue
    if ($cmd) {
        Say "found  Go: $($cmd.Source)"
        return $cmd.Source
    }

    $goRoot = Join-Path $ToolsDir "go"
    $goExe = Join-Path $goRoot "bin/go.exe"
    if (Test-Path -LiteralPath $goExe) {
        $env:PATH = "$(Join-Path $goRoot "bin");$env:PATH"
        Say "found  bundled Go: $goExe"
        return $goExe
    }

    $arch = Get-GoArch
    $downloads = Join-Path $ToolsDir "downloads"
    Ensure-Dir $downloads
    $zip = Join-Path $downloads "go$GoVersion.windows-$arch.zip"
    $url = "https://go.dev/dl/go$GoVersion.windows-$arch.zip"

    Say "Go not found; downloading $url"
    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
    Invoke-WebRequest -Uri $url -OutFile $zip -UseBasicParsing

    if (Test-Path -LiteralPath $goRoot) {
        Remove-Item -LiteralPath $goRoot -Recurse -Force
    }
    Expand-Archive -LiteralPath $zip -DestinationPath $ToolsDir -Force
    if (-not (Test-Path -LiteralPath $goExe)) {
        throw "Downloaded Go archive did not create $goExe"
    }
    $env:PATH = "$(Join-Path $goRoot "bin");$env:PATH"
    Say "ready  bundled Go: $goExe"
    return $goExe
}

function Copy-IfMissing([string]$Source, [string]$Destination) {
    if ((Test-Path -LiteralPath $Destination) -and -not $Force) {
        Say "keep   $Destination"
        return $false
    }
    Ensure-Dir (Split-Path -Parent $Destination)
    Copy-Item -LiteralPath $Source -Destination $Destination -Force
    Say "write  $Destination"
    return $true
}

function Replace-Text([string]$Path, [string]$Old, [string]$New) {
    if (-not (Test-Path -LiteralPath $Path)) {
        return
    }
    $text = Get-Content -LiteralPath $Path -Raw
    if ($text.Contains($Old)) {
        $text = $text.Replace($Old, $New)
        Set-Content -LiteralPath $Path -Value $text -NoNewline
    }
}

function Copy-PluginConfigs([string]$BaseDir) {
    if (-not (Test-Path -LiteralPath $BaseDir)) {
        return
    }
    Get-ChildItem -LiteralPath $BaseDir -Recurse -Filter "config.yml.dist" | ForEach-Object {
        $dest = Join-Path $_.DirectoryName "config.yml"
        if ((Test-Path -LiteralPath $dest) -and -not $Force) {
            Say "keep   $dest"
        } else {
            Copy-Item -LiteralPath $_.FullName -Destination $dest -Force
            Say "write  $dest"
        }
    }
}

function Write-RunScripts {
    $master = @'
Set-Location $PSScriptRoot
.\weaveftpd.exe --config etc/config.yml
'@
    $slave = @'
Set-Location $PSScriptRoot
.\weaveftpd.exe --config etc/config-slave.yml
'@
    $sitebot = @'
Set-Location (Join-Path $PSScriptRoot "sitebot")
.\sitebot.exe --config etc/config.yml
'@
    Set-Content -LiteralPath (Join-Path $Root "run-master.ps1") -Value $master
    Set-Content -LiteralPath (Join-Path $Root "run-slave.ps1") -Value $slave
    Set-Content -LiteralPath (Join-Path $Root "run-sitebot.ps1") -Value $sitebot
    Say "write  run-master.ps1"
    Say "write  run-slave.ps1"
    Say "write  run-sitebot.ps1"
}

function Install-Configs {
    Say "Preparing directories and configs..."
    foreach ($dir in @("logs", "site", "userdata", "etc/certs", "sitebot/logs")) {
        Ensure-Dir (Join-Path $Root $dir)
    }

    $eventFile = Join-Path $Root "etc/weaveftpd.sitebot.events.log"
    if (-not (Test-Path -LiteralPath $eventFile)) {
        New-Item -ItemType File -Path $eventFile | Out-Null
        Say "write  $eventFile"
    } else {
        Say "keep   $eventFile"
    }

    if ($Config -eq "master" -or $Config -eq "both") {
        Copy-IfMissing (Join-Path $Root "etc/config-example.yml") (Join-Path $Root "etc/config.yml") | Out-Null
    }
    if ($Config -eq "slave" -or $Config -eq "both") {
        Copy-IfMissing (Join-Path $Root "etc/config-slave-example.yml") (Join-Path $Root "etc/config-slave.yml") | Out-Null
    }
    Copy-IfMissing (Join-Path $Root "sitebot/etc/config.yml.example") (Join-Path $Root "sitebot/etc/config.yml") | Out-Null

    $eventConfigPath = To-ConfigPath $eventFile
    $sitebotConfigPath = To-ConfigPath (Join-Path $Root "sitebot/etc/config.yml")
    foreach ($cfg in @((Join-Path $Root "etc/config.yml"), (Join-Path $Root "sitebot/etc/config.yml"))) {
        Replace-Text $cfg "/WeaveFTPd/etc/weaveftpd.sitebot.fifo" $eventConfigPath
        Replace-Text $cfg "/WeaveFTPd/etc/weaveftpd.sitebot.events.log" $eventConfigPath
    }
    Replace-Text (Join-Path $Root "etc/config.yml") "/WeaveFTPd/sitebot/etc/config.yml" $sitebotConfigPath

    Copy-PluginConfigs (Join-Path $Root "plugins")
    Copy-PluginConfigs (Join-Path $Root "sitebot/plugins")
    Write-RunScripts
}

function Build-Binaries {
    $goExe = Ensure-Go
    Say "Building weaveftpd.exe..."
    & $goExe build -o (Join-Path $Root "weaveftpd.exe") ./cmd/weaveftpd

    Say "Building sitebot.exe..."
    Push-Location (Join-Path $Root "sitebot")
    try {
        & $goExe build -o (Join-Path $Root "sitebot/sitebot.exe") ./cmd
    } finally {
        Pop-Location
    }
}

function Generate-CertsWithGo([string]$GoExe, [string]$CertDir) {
    Ensure-Dir $ToolsDir
    $helper = Join-Path $ToolsDir "makecerts.go"
    $source = @'
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

func main() {
	if len(os.Args) != 2 {
		log.Fatal("usage: makecerts <cert-dir>")
	}
	dir := os.Args[1]
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Fatal(err)
	}

	caKey := mustKey()
	serverKey := mustKey()
	clientKey := mustKey()

	now := time.Now().Add(-time.Hour)
	ca := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{CommonName: "WeaveFTPd Root CA", Organization: []string{"WeaveFTPd"}},
		NotBefore: now,
		NotAfter: now.AddDate(10, 0, 0),
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA: true,
	}
	server := leaf(2, "WeaveFTPd FTP", x509.ExtKeyUsageServerAuth)
	server.DNSNames = []string{"localhost"}
	server.IPAddresses = []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
	client := leaf(3, "WeaveFTPd Slave", x509.ExtKeyUsageClientAuth)

	writeCert(filepath.Join(dir, "ca.crt"), mustCert(ca, ca, &caKey.PublicKey, caKey))
	writeKey(filepath.Join(dir, "ca.key"), caKey)
	writeCert(filepath.Join(dir, "server.crt"), mustCert(server, ca, &serverKey.PublicKey, caKey))
	writeKey(filepath.Join(dir, "server.key"), serverKey)
	writeCert(filepath.Join(dir, "client.crt"), mustCert(client, ca, &clientKey.PublicKey, caKey))
	writeKey(filepath.Join(dir, "client.key"), clientKey)
}

func leaf(serial int64, cn string, usage x509.ExtKeyUsage) *x509.Certificate {
	now := time.Now().Add(-time.Hour)
	return &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject: pkix.Name{CommonName: cn, Organization: []string{"WeaveFTPd"}},
		NotBefore: now,
		NotAfter: now.AddDate(10, 0, 0),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{usage},
		BasicConstraintsValid: true,
	}
}

func mustKey() *ecdsa.PrivateKey {
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	return key
}

func mustCert(tmpl, parent *x509.Certificate, pub interface{}, parentKey interface{}) []byte {
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, pub, parentKey)
	if err != nil {
		log.Fatal(err)
	}
	return der
}

func writeCert(path string, der []byte) {
	data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(path, data, 0644); err != nil {
		log.Fatal(err)
	}
}

func writeKey(path string, key *ecdsa.PrivateKey) {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		log.Fatal(err)
	}
	data := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	if err := os.WriteFile(path, data, 0600); err != nil {
		log.Fatal(err)
	}
}
'@
    Set-Content -LiteralPath $helper -Value $source -NoNewline
    Say "OpenSSL not found; generating TLS certs with Go."
    & $GoExe run $helper $CertDir
}

function Generate-Certs {
    $certDir = Join-Path $Root "etc/certs"
    $serverCert = Join-Path $certDir "server.crt"
    $serverKey = Join-Path $certDir "server.key"
    if ((Test-Path -LiteralPath $serverCert) -and (Test-Path -LiteralPath $serverKey) -and -not $Force) {
        Say "keep   TLS certs in $certDir"
        return
    }

    $opensslCmd = Get-Command openssl -ErrorAction SilentlyContinue
    if (-not $opensslCmd) {
        $goExe = Ensure-Go
        Generate-CertsWithGo $goExe $certDir
        return
    }

    Ensure-Dir $certDir
    Push-Location $certDir
    try {
        Say "Generating TLS certs in $certDir..."
        & $opensslCmd.Source ecparam -genkey -name secp384r1 -out ca.key
        & $opensslCmd.Source req -new -x509 -sha384 -days 3650 -key ca.key -out ca.crt -subj "/CN=WeaveFTPd Root CA/O=WeaveFTPd"
        & $opensslCmd.Source ecparam -genkey -name secp384r1 -out server.key
        & $opensslCmd.Source req -new -sha384 -key server.key -out server.csr -subj "/CN=WeaveFTPd FTP/O=WeaveFTPd"
        & $opensslCmd.Source x509 -req -sha384 -days 3650 -in server.csr -CA ca.crt -CAkey ca.key -CAcreateserial -out server.crt
        & $opensslCmd.Source ecparam -genkey -name secp384r1 -out client.key
        & $opensslCmd.Source req -new -sha384 -key client.key -out client.csr -subj "/CN=WeaveFTPd Slave/O=WeaveFTPd"
        & $opensslCmd.Source x509 -req -sha384 -days 3650 -in client.csr -CA ca.crt -CAkey ca.key -CAcreateserial -out client.crt
        Remove-Item -LiteralPath "server.csr", "client.csr", "ca.srl" -Force -ErrorAction SilentlyContinue
    } finally {
        Pop-Location
    }
}

Say "=============================================================="
Say "WeaveFTPd Windows console setup"
Say "=============================================================="

switch ($Mode) {
    "configs" {
        Install-Configs
    }
    "certs" {
        Generate-Certs
    }
    "build" {
        Build-Binaries
    }
    "install" {
        Install-Configs
        if (-not $SkipCerts) {
            Generate-Certs
        }
        if (-not $SkipBuild) {
            Build-Binaries
        }
    }
}

Say ""
Say "Console start commands:"
Say "  .\run-master.ps1"
Say "  .\run-slave.ps1"
Say "  .\run-sitebot.ps1"
Say ""
Say "Windows event note:"
Say "  event_fifo points at a regular append-only event file on Windows."
Say "  The daemon appends JSON lines; the sitebot tails only new lines."
Say ""
Say "Dependency note:"
Say "  If go.exe is missing, setup downloads Go $GoVersion into .tools\go."
Say "  OpenSSL is optional; certs use OpenSSL when present, otherwise Go."
