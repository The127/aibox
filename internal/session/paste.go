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
	modifierAlt      = 2
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

// key is a paste key the watcher saw.
type key int

const (
	noKey key = iota
	ctrlVKey
	altVKey
)

// pasteWatcher passes on what the person types and calls onPaste for each
// paste key in it: Ctrl+V as the control byte, in the kitty keyboard
// protocol and with modifyOtherKeys of xterm, and with altV Alt+V the same
// ways. onPaste returns what goes on in place of the key, such as the text
// on the clipboard, or nil to pass the key on, as Ctrl+V for Alt+V. The
// watcher looks at the bytes one by one, so that a key split across two
// reads counts too, but only a key a read holds whole can be replaced:
// holding back an escape until the next read would hold back the Escape
// key too.
type pasteWatcher struct {
	r       io.Reader
	onPaste func() []byte
	altV    bool
	// sequence is the escape sequence read so far, without the escape, or
	// nil outside of one
	sequence []byte
	// from is where the escape of the sequence is in what the read passes
	// on, or -1 when an earlier read passed it on already
	from    int
	pasting bool
	// pending is what a read passed on that did not fit, as a paste can be
	// longer than its key, and err the error of that read
	pending []byte
	err     error
}

func watchPaste(r io.Reader, onPaste func() []byte, altV bool) io.Reader {
	return &pasteWatcher{r: r, onPaste: onPaste, altV: altV, from: -1}
}

func (p *pasteWatcher) Read(b []byte) (int, error) {
	for len(p.pending) == 0 && p.err == nil {
		n, err := p.r.Read(b)
		p.pending, p.err = p.scan(b[:n]), err
	}

	n := copy(b, p.pending)
	p.pending = p.pending[n:]

	if len(p.pending) > 0 {
		return n, nil
	}

	err := p.err
	p.err = nil

	return n, err
}

// scan returns what goes on of what the read got.
func (p *pasteWatcher) scan(in []byte) []byte {
	out := make([]byte, 0, len(in))

	for _, c := range in {
		if c == esc {
			p.from = len(out)
		}

		out = append(out, c)

		k := p.see(c)
		if k == noKey {
			continue
		}

		start := p.from
		if c == ctrlV {
			start = len(out) - 1
		}

		// what the key began with went on already, so it goes on as it
		// is, which for Alt+V is no paste key
		if start < 0 && k == altVKey {
			continue
		}

		pasted := p.onPaste()

		switch {
		case start < 0:
		case pasted != nil:
			out = append(out[:start], pasted...)
		case k == altVKey:
			out = append(out[:start], ctrlV)
		}
	}

	p.from = -1

	return out
}

// see looks at the next byte, and tells whether it ends a paste key.
func (p *pasteWatcher) see(c byte) key {
	switch {
	case c == esc:
		p.sequence = []byte{}
	case p.sequence == nil:
		if c == ctrlV && !p.pasting {
			return ctrlVKey
		}
	case c < ' ' || len(p.sequence) == longestSequence:
		// no sequence the watcher reads holds a control byte, so this one
		// is a key of its own
		p.sequence = nil

		return p.see(c)
	default:
		p.sequence = append(p.sequence, c)

		switch p.sequence[0] {
		case '[':
			// a CSI sequence ends with a byte from @ to ~
			if len(p.sequence) > 1 && c >= '@' && c <= '~' {
				sequence := string(p.sequence)
				p.sequence = nil

				return p.end(sequence)
			}
		case 'O':
			// SS3, as for F1, has one more byte
			if len(p.sequence) == 2 {
				p.sequence = nil
			}
		default:
			// Alt and a key
			p.sequence = nil

			if p.altV && !p.pasting && c == 'v' {
				return altVKey
			}
		}
	}

	return noKey
}

func (p *pasteWatcher) end(sequence string) key {
	switch {
	case sequence == pasteStart:
		p.pasting = true
	case sequence == pasteEnd:
		p.pasting = false
	case p.pasting:
	case isKeyV(sequence, modifierCtrl):
		return ctrlVKey
	case p.altV && isKeyV(sequence, modifierAlt):
		return altVKey
	}

	return noKey
}

// isKeyV tells whether a CSI sequence is V with the modifier going down:
// [118;5u for Ctrl in the kitty protocol, maybe with :1 for the press, or
// [27;5;118~ with modifyOtherKeys. Caps Lock and Num Lock may be on.
func isKeyV(sequence string, modifier int) bool {
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

		return (event == "" || event == "1") && only(modifiers, modifier)
	case '~':
		fields := strings.Split(body, ";")

		return len(fields) == 3 && fields[0] == "27" && fields[2] == keyV && only(fields[1], modifier)
	default:
		return false
	}
}

// only tells whether the modifiers of the field are the one alone.
func only(field string, modifier int) bool {
	value, err := strconv.Atoi(field)
	if err != nil || value < 1 {
		return false
	}

	return (value-1)&^(modifierCapsLock|modifierNumLock) == modifier
}
