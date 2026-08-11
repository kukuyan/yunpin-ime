# SPDX-License-Identifier: GPL-3.0-only
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$Destination
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
    throw "New-PreviewIcon.ps1 requires Windows GDI+"
}
Add-Type -AssemblyName System.Drawing

function New-YunPinPng {
    param([Parameter(Mandatory = $true)][int]$Size)
    $bitmap = [System.Drawing.Bitmap]::new($Size, $Size, [System.Drawing.Imaging.PixelFormat]::Format32bppArgb)
    $graphics = [System.Drawing.Graphics]::FromImage($bitmap)
    $blue = [System.Drawing.SolidBrush]::new([System.Drawing.ColorTranslator]::FromHtml("#3478F6"))
    $white = [System.Drawing.Pen]::new([System.Drawing.Color]::White, [single]18)
    $white.StartCap = [System.Drawing.Drawing2D.LineCap]::Round
    $white.EndCap = [System.Drawing.Drawing2D.LineCap]::Round
    try {
        $graphics.Clear([System.Drawing.Color]::Transparent)
        $graphics.SmoothingMode = [System.Drawing.Drawing2D.SmoothingMode]::AntiAlias
        $scale = $Size / 256.0
        $graphics.ScaleTransform($scale, $scale)

        # Original YunPin cloud/phrase mark. The overlapping primitives form a
        # single silhouette after rasterization and use no upstream artwork.
        $graphics.FillEllipse($blue, 20, 92, 92, 92)
        $graphics.FillEllipse($blue, 58, 32, 142, 142)
        $graphics.FillEllipse($blue, 145, 82, 94, 94)
        $graphics.FillRectangle($blue, 64, 104, 132, 80)
        $graphics.FillRectangle($blue, 105, 150, 46, 55)
        $graphics.FillEllipse($blue, 105, 182, 46, 46)
        $graphics.DrawLine($white, 84, 109, 172, 109)
        $graphics.DrawLine($white, 67, 143, 189, 143)

        $stream = [IO.MemoryStream]::new()
        try {
            $bitmap.Save($stream, [System.Drawing.Imaging.ImageFormat]::Png)
            return $stream.ToArray()
        } finally {
            $stream.Dispose()
        }
    } finally {
        $white.Dispose()
        $blue.Dispose()
        $graphics.Dispose()
        $bitmap.Dispose()
    }
}

$sizes = @(16, 20, 24, 32, 40, 48, 64, 128, 256)
$images = @()
foreach ($size in $sizes) {
    $images += ,([ordered]@{ size = $size; bytes = (New-YunPinPng -Size $size) })
}

$Destination = [IO.Path]::GetFullPath($Destination)
New-Item -ItemType Directory -Path (Split-Path $Destination -Parent) -Force | Out-Null
$stream = [IO.File]::Open($Destination, [IO.FileMode]::Create, [IO.FileAccess]::Write, [IO.FileShare]::None)
$writer = [IO.BinaryWriter]::new($stream)
try {
    $writer.Write([uint16]0)
    $writer.Write([uint16]1)
    $writer.Write([uint16]$images.Count)
    $offset = 6 + (16 * $images.Count)
    foreach ($image in $images) {
        $dimension = if ($image.size -eq 256) { 0 } else { $image.size }
        $writer.Write([byte]$dimension)
        $writer.Write([byte]$dimension)
        $writer.Write([byte]0)
        $writer.Write([byte]0)
        $writer.Write([uint16]1)
        $writer.Write([uint16]32)
        $writer.Write([uint32]$image.bytes.Length)
        $writer.Write([uint32]$offset)
        $offset += $image.bytes.Length
    }
    foreach ($image in $images) {
        $writer.Write([byte[]]$image.bytes)
    }
} finally {
    $writer.Dispose()
    $stream.Dispose()
}

Write-Host "Generated original YunPin preview icon: $Destination"
