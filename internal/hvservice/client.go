package hvservice

// Client is who is on the other end of the pipe, as its token says, never
// as it says itself.
type Client struct {
	// User is the SID of the user.
	User string
	// AppContainer is the SID of the AppContainer the client runs in, or
	// empty.
	AppContainer string
}

// SIDs are the SIDs the Hyper-V sockets of the client's VM open to. A
// client in an AppContainer reaches a socket by its AppContainer SID.
func (c Client) SIDs() []string {
	if c.AppContainer == "" {
		return []string{c.User}
	}

	return []string{c.User, c.AppContainer}
}
