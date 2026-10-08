package gitconfig

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// ErrNoSSHKey is a server for which the host has no SSH key aibox can use
// without asking: none in the agent, and none of the key files ssh would
// try that needs no passphrase.
var ErrNoSSHKey = errors.New("this machine has no SSH key for it that aibox can use, in the SSH agent or in a key file without a passphrase")

// ErrUnknownHost is a server that is not in the known hosts of the host.
var ErrUnknownHost = errors.New("the server is not in the known hosts of this machine, connect once with ssh to add it")

// ErrProxy is a host the ssh config reaches through another host or a
// command, which aibox cannot do.
var ErrProxy = errors.New("the ssh config reaches it with ProxyJump or ProxyCommand, which aibox does not do")

// sshConfigTimeout is how long ssh may take to print its config.
const sshConfigTimeout = 10 * time.Second

// SSHKeys find how the host reaches a server over SSH, as ssh would. They
// are used before the VM starts, since aibox can open no files once it is
// confined. The keys of an agent go through a connection to it that stays
// open until Close.
type SSHKeys struct {
	// Config is the ssh config file, or empty for the one ssh reads.
	Config string

	home   string
	agents map[string]*sshAgent
}

// sshAgent is the connection to one agent and its keys.
type sshAgent struct {
	conn    net.Conn
	signers []ssh.Signer
}

// SSHTarget is how aibox reaches a server over SSH, as ssh would.
type SSHTarget struct {
	// Address is the host name and port to dial, which the ssh config
	// may change.
	Address string
	User    string
	Signers []ssh.Signer
	// HostKey checks the key of the server and says what is wrong with it.
	HostKey ssh.HostKeyCallback
	// HostKeyAlgorithms are those of the keys the known hosts hold for the
	// server.
	HostKeyAlgorithms []string
	// Keys name the keys, for the person to see which ones aibox uses.
	Keys []string
}

// LoadSSHKeys returns the SSH keys of the host, which reach agents and
// read files only when asked For a host.
func LoadSSHKeys() (*SSHKeys, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	return &SSHKeys{home: home, agents: map[string]*sshAgent{}}, nil
}

// For returns how ssh would reach the host: the host name, port, user,
// agent and known hosts of the ssh config, and the keys it would offer, of
// those that need no passphrase or are in the agent.
func (k *SSHKeys) For(ctx context.Context, host string) (SSHTarget, error) {
	config, err := k.sshConfig(ctx, host)
	if err != nil {
		return SSHTarget{}, err
	}

	if config.proxy {
		return SSHTarget{}, fmt.Errorf("%s: %w", host, ErrProxy)
	}

	known, err := knownHosts(config.knownHosts)
	if err != nil {
		return SSHTarget{}, err
	}

	// the known hosts name the server by its alias, when the config gives
	// one, and the port when it is not 22
	address := net.JoinHostPort(config.hostname, config.port)
	knownAs := address

	if config.hostKeyAlias != "" {
		knownAs = net.JoinHostPort(config.hostKeyAlias, config.port)
	}

	check := NewHostKeyCheck(known)
	target := SSHTarget{
		Address: address,
		User:    config.user,
		HostKey: func(_ string, remote net.Addr, key ssh.PublicKey) error {
			return check(knownAs, remote, key)
		},
	}

	if target.HostKeyAlgorithms, err = HostKeyAlgorithms(known, knownAs); err != nil {
		return SSHTarget{}, err
	}

	agentKeys := k.agent(config.identityAgent)

	add := func(signer ssh.Signer, name string) {
		key := signer.PublicKey().Marshal()
		if !slices.ContainsFunc(target.Signers, func(s ssh.Signer) bool { return bytes.Equal(s.PublicKey().Marshal(), key) }) {
			target.Signers = append(target.Signers, signer)
			target.Keys = append(target.Keys, name)
		}
	}

	// as ssh, the key files first, from the agent when it holds them, and
	// then the other keys of the agent unless the config says otherwise
	for _, file := range config.identityFiles {
		if signer, ok := fromAgent(agentKeys, file); ok {
			add(signer, file)

			continue
		}

		content, err := os.ReadFile(file) //nolint:gosec // a key file the ssh config names
		if err != nil {
			continue
		}

		if signer, err := ssh.ParsePrivateKey(content); err == nil {
			add(signer, file)
		}
	}

	if !config.identitiesOnly {
		for _, signer := range agentKeys {
			add(signer, ssh.FingerprintSHA256(signer.PublicKey())+" in the SSH agent")
		}
	}

	if len(target.Signers) == 0 {
		return SSHTarget{}, ErrNoSSHKey
	}

	return target, nil
}

// agent returns the keys of the agent at the socket, which it connects to
// once. An agent that does not answer has no keys.
func (k *SSHKeys) agent(socket string) []ssh.Signer {
	if socket == "" {
		return nil
	}

	if known, ok := k.agents[socket]; ok {
		return known.signers
	}

	a := &sshAgent{}
	k.agents[socket] = a

	conn, err := net.Dial("unix", socket)
	if err != nil {
		return nil
	}

	signers, err := agent.NewClient(conn).Signers()
	if err != nil || len(signers) == 0 {
		_ = conn.Close()

		return nil
	}

	a.conn, a.signers = conn, signers

	return signers
}

// fromAgent returns the key of the agent whose public key is in the .pub
// file next to the key file.
func fromAgent(signers []ssh.Signer, file string) (ssh.Signer, bool) {
	content, err := os.ReadFile(file + ".pub") //nolint:gosec // the public key next to a key file the ssh config names
	if err != nil {
		return nil, false
	}

	public, _, _, _, err := ssh.ParseAuthorizedKey(content)
	if err != nil {
		return nil, false
	}

	for _, signer := range signers {
		if bytes.Equal(signer.PublicKey().Marshal(), public.Marshal()) {
			return signer, true
		}
	}

	return nil, false
}

// Close closes the connections to the agents.
func (k *SSHKeys) Close() {
	for _, a := range k.agents {
		if a.conn != nil {
			_ = a.conn.Close()
		}
	}
}

// knownHosts reads those of the files that exist.
func knownHosts(files []string) (ssh.HostKeyCallback, error) {
	var existing []string

	for _, file := range files {
		if _, err := os.Stat(file); err == nil {
			existing = append(existing, file)
		}
	}

	known, err := knownhosts.New(existing...)
	if err != nil {
		return nil, fmt.Errorf("read the known hosts: %w", err)
	}

	return known, nil
}

// resolved is what the ssh config says about a host.
type resolved struct {
	hostname, port, user string
	hostKeyAlias         string
	identityAgent        string
	identityFiles        []string
	knownHosts           []string
	identitiesOnly       bool
	proxy                bool
}

// sshConfig asks ssh how it would reach the host. Without ssh, aibox goes
// to port 22 as git with the default key files, agent and known hosts.
func (k *SSHKeys) sshConfig(ctx context.Context, host string) (resolved, error) {
	config := resolved{
		hostname: host, port: "22", user: "git",
		identityAgent: os.Getenv("SSH_AUTH_SOCK"),
		knownHosts:    []string{filepath.Join(k.home, ".ssh", "known_hosts"), "/etc/ssh/ssh_known_hosts"},
	}

	ctx, cancel := context.WithTimeout(ctx, sshConfigTimeout)
	defer cancel()

	args := []string{"-G"}
	if k.Config != "" {
		args = append(args, "-F", k.Config)
	}

	cmd := exec.CommandContext(ctx, "ssh", append(args, "git@"+host)...) //nolint:gosec // the host is one of the config, checked when it was read
	cmd.Dir = "/"

	out, err := cmd.Output()
	if errors.Is(err, exec.ErrNotFound) {
		for _, name := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
			config.identityFiles = append(config.identityFiles, filepath.Join(k.home, ".ssh", name))
		}

		return config, nil
	}

	if err != nil {
		return resolved{}, fmt.Errorf("ask ssh how it reaches %s: %w", host, err)
	}

	config.knownHosts = nil

	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		key, value, _ := strings.Cut(scanner.Text(), " ")

		switch key {
		case "hostname":
			config.hostname = value
		case "port":
			if n, err := strconv.Atoi(value); err == nil && n > 0 && n < 65536 {
				config.port = value
			}
		case "user":
			config.user = value
		case "hostkeyalias":
			config.hostKeyAlias = value
		case "identitiesonly":
			config.identitiesOnly = value == "yes"
		case "identityfile":
			if file, ok := k.path(value); ok {
				config.identityFiles = append(config.identityFiles, file)
			}
		case "identityagent":
			config.identityAgent = k.agentSocket(value)
		case "userknownhostsfile", "globalknownhostsfile":
			for _, file := range strings.Fields(value) {
				if path, ok := k.path(file); ok {
					config.knownHosts = append(config.knownHosts, path)
				}
			}
		case "proxyjump", "proxycommand":
			config.proxy = value != "none"
		}
	}

	return config, nil
}

// agentSocket is the socket an IdentityAgent of the ssh config names.
func (k *SSHKeys) agentSocket(value string) string {
	switch value {
	case "none":
		return ""
	case "SSH_AUTH_SOCK", "$SSH_AUTH_SOCK":
		return os.Getenv("SSH_AUTH_SOCK")
	}

	path, _ := k.path(value)

	return path
}

// path expands a ~ at the start of a path of the ssh config, and is false
// for a path that is not absolute then.
func (k *SSHKeys) path(value string) (string, bool) {
	if rest, ok := strings.CutPrefix(value, "~/"); ok {
		value = filepath.Join(k.home, rest)
	}

	return value, filepath.IsAbs(value)
}

// NewHostKeyCheck returns the check of the known hosts that says what is
// wrong with the key of a server in words the person can act on.
func NewHostKeyCheck(known ssh.HostKeyCallback) ssh.HostKeyCallback {
	return func(address string, remote net.Addr, key ssh.PublicKey) error {
		err := known(address, remote, key)

		var keyErr *knownhosts.KeyError

		switch {
		case errors.As(err, &keyErr) && len(keyErr.Want) == 0:
			return fmt.Errorf("%s: %w", address, ErrUnknownHost)
		case errors.As(err, &keyErr):
			return fmt.Errorf("%s: the key of the server is not the one in the known hosts of this machine", address)
		}

		return err
	}
}

// HostKeyAlgorithms returns the algorithms of the keys the known hosts
// hold for the address, so that the server is asked for one of those and
// not for a type the known hosts do not hold.
func HostKeyAlgorithms(known ssh.HostKeyCallback, address string) ([]string, error) {
	// a key of nobody, which the check refuses, names the keys it knows
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}

	public, err := ssh.NewPublicKey(key.Public())
	if err != nil {
		return nil, err
	}

	err = known(address, &net.TCPAddr{}, public)

	var keyErr *knownhosts.KeyError
	if !errors.As(err, &keyErr) || len(keyErr.Want) == 0 {
		return nil, fmt.Errorf("%s: %w", address, ErrUnknownHost)
	}

	var algorithms []string

	for _, known := range keyErr.Want {
		for _, algorithm := range keyAlgorithms(known.Key.Type()) {
			if !slices.Contains(algorithms, algorithm) {
				algorithms = append(algorithms, algorithm)
			}
		}
	}

	return algorithms, nil
}

// keyAlgorithms are the algorithms a server signs with for a key type. An
// RSA key signs with SHA-2 today, and with SHA-1 on old servers.
func keyAlgorithms(keyType string) []string {
	if keyType == ssh.KeyAlgoRSA {
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
	}

	return []string{keyType}
}
