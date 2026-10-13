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

// textScript writes the text on the clipboard to standard output as UTF-8,
// whatever the code page of the console.
const textScript = `Add-Type -AssemblyName System.Windows.Forms
$text = [System.Windows.Forms.Clipboard]::GetText()
if ([string]::IsNullOrEmpty($text)) { [Console]::Error.Write('no text'); exit 1 }
$bytes = [System.Text.Encoding]::UTF8.GetBytes($text)
$out = [Console]::OpenStandardOutput()
$out.Write($bytes, 0, $bytes.Length)
$out.Flush()`

// readText gets the text from PowerShell, as read does the image.
func readText() ([]byte, error) {
	return output("powershell", "-NoProfile", "-NonInteractive", "-Sta", "-Command", textScript)
}
