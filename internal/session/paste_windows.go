package session

// altVPastes takes Alt+V for Ctrl+V: Windows Terminal and the Windows
// console paste on Ctrl+V themselves, so it never reaches aibox, and
// Claude Code on Windows pastes an image on Alt+V for that reason.
const altVPastes = true
