package session

import (
	"io"
	"strconv"
	"strings"
)

const (
	ctrlV = 0x16
	esc   = 0x1b
	// the key code of v in the kitty keyboard protocol and in
	// modifyOtherKeys of xterm
	keyV = "118"
	// the bits of the modifiers, less one, as both encode them
	modifierCtrl     = 4
	modifierCapsLock = 64
	modifierNumLock  = 128
)

// What a terminal sends around text pasted into it in bracketed paste
// mode. A Ctrl+V in that text is no key press.
const (
	pasteStart = "[200~"
	pasteEnd   = "[201~"
)

// longestSequence is longer than any escape sequence the watcher reads,
// such as "\x1b[27;133;118~".
const longestSequence = 16

// pasteWatcher passes on what the person types and calls onPaste for each
// Ctrl+V in it: as the control byte, in the kitty keyboard protocol and
// with modifyOtherKeys of xterm. It looks at the bytes one by one, so that
// a sequence split across two reads counts too.
type pasteWatcher struct {
	r       io.Reader
	onPaste func()
	// sequence is the escape sequence read so far, without the escape, or
	// nil outside of one
	sequence []byte
	pasting  bool
}

func watchPaste(r io.Reader, onPaste func()) io.Reader {
	return &pasteWatcher{r: r, onPaste: onPaste}
}

func (p *pasteWatcher) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)

	for _, c := range b[:n] {
		p.see(c)
	}

	return n, err
}

func (p *pasteWatcher) see(c byte) {
	switch {
	case c == esc:
		p.sequence = []byte{}
	case p.sequence == nil:
		if c == ctrlV && !p.pasting {
			p.onPaste()
		}
	case c < ' ' || len(p.sequence) == longestSequence:
		// no sequence the watcher reads holds a control byte, so this one
		// is a key of its own
		p.sequence = nil
		p.see(c)
	default:
		p.sequence = append(p.sequence, c)

		switch p.sequence[0] {
		case '[':
			// a CSI sequence ends with a byte from @ to ~
			if len(p.sequence) > 1 && c >= '@' && c <= '~' {
				p.end(string(p.sequence))
				p.sequence = nil
			}
		case 'O':
			// SS3, as for F1, has one more byte
			if len(p.sequence) == 2 {
				p.sequence = nil
			}
		default:
			// Alt and a key
			p.sequence = nil
		}
	}
}

func (p *pasteWatcher) end(sequence string) {
	switch {
	case sequence == pasteStart:
		p.pasting = true
	case sequence == pasteEnd:
		p.pasting = false
	case !p.pasting && isCtrlV(sequence):
		p.onPaste()
	}
}

// isCtrlV tells whether a CSI sequence is Ctrl+V going down: [118;5u in
// the kitty protocol, maybe with :1 for the press, or [27;5;118~ with
// modifyOtherKeys. Caps Lock and Num Lock may be on.
func isCtrlV(sequence string) bool {
	body := sequence[1 : len(sequence)-1]

	switch sequence[len(sequence)-1] {
	case 'u':
		key, rest, ok := strings.Cut(body, ";")

		// the key may come with the shifted key after a colon
		key, _, _ = strings.Cut(key, ":")
		if !ok || key != keyV {
			return false
		}

		modifiers, event, _ := strings.Cut(rest, ":")

		return (event == "" || event == "1") && onlyCtrl(modifiers)
	case '~':
		fields := strings.Split(body, ";")

		return len(fields) == 3 && fields[0] == "27" && fields[2] == keyV && onlyCtrl(fields[1])
	default:
		return false
	}
}

func onlyCtrl(field string) bool {
	value, err := strconv.Atoi(field)
	if err != nil || value < 1 {
		return false
	}

	return (value-1)&^(modifierCapsLock|modifierNumLock) == modifierCtrl
}
