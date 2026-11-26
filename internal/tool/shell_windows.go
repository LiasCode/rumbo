//go:build windows

package tool

// shell is Windows PowerShell, which every Windows has. Its output is set
// to UTF-8 first, since a pipe otherwise gets the console's code page, and
// its lines end with CR LF.
var shell = shellProgram{
	name:    "powershell",
	argv:    []string{"powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command"},
	prefix:  "[Console]::OutputEncoding = [Text.Encoding]::UTF8; ",
	stopped: "taskkill /T /F /PID %d",
	crlf:    true,
}
