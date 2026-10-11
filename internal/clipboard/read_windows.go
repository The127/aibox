package clipboard

// script writes the image on the clipboard as PNG to standard output, as
// Claude Code reads it on Windows.
const script = `Add-Type -AssemblyName System.Windows.Forms
$image = [System.Windows.Forms.Clipboard]::GetImage()
if ($null -eq $image) { [Console]::Error.Write('no image'); exit 1 }
$png = New-Object System.IO.MemoryStream
$image.Save($png, [System.Drawing.Imaging.ImageFormat]::Png)
$out = [Console]::OpenStandardOutput()
$out.Write($png.ToArray(), 0, $png.Length)
$out.Flush()`

// read gets the image as PNG from PowerShell. The clipboard needs a
// single threaded apartment, which -Sta gives it.
func read() ([]byte, error) {
	return output("powershell", "-NoProfile", "-NonInteractive", "-Sta", "-Command", script)
}
