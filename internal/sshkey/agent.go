package sshkey

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// Agent is an ephemeral in-process SSH agent holding one deploy key. Its unix
// socket is chowned to the site user and destroyed when the deploy finishes, so
// the private key never touches that user's disk — an old implementation copies it into
// every site user's ~/.ssh and only removes it on repo:delete.
type Agent struct {
	dir      string
	socket   string
	listener net.Listener
	closeOne sync.Once
}

// StartAgent serves priv on a fresh unix socket owned by uid:gid.
func StartAgent(priv []byte, uid, gid int) (*Agent, error) {
	raw, err := ssh.ParseRawPrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("parse deploy key: %w", err)
	}

	dir, err := os.MkdirTemp("", "rec-deploy-agent-")
	if err != nil {
		return nil, fmt.Errorf("create agent dir: %w", err)
	}

	a := &Agent{dir: dir, socket: filepath.Join(dir, "agent.sock")}

	keyring := agent.NewKeyring()
	if err := keyring.Add(agent.AddedKey{PrivateKey: raw, Comment: "rec-deploy"}); err != nil {
		_ = a.Close()
		return nil, fmt.Errorf("load deploy key into agent: %w", err)
	}

	if a.listener, err = net.Listen("unix", a.socket); err != nil {
		_ = a.Close()
		return nil, fmt.Errorf("listen on agent socket: %w", err)
	}

	// The socket is the only thing the site user ever sees of the key. The
	// directory guards it: 0700 owned by that user, so nobody else can connect.
	if err := os.Chown(dir, uid, gid); err != nil {
		_ = a.Close()
		return nil, fmt.Errorf("chown agent dir to %d:%d: %w", uid, gid, err)
	}
	if err := os.Chown(a.socket, uid, gid); err != nil {
		_ = a.Close()
		return nil, fmt.Errorf("chown agent socket to %d:%d: %w", uid, gid, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = a.Close()
		return nil, fmt.Errorf("restrict agent dir: %w", err)
	}

	go a.serve(keyring)

	return a, nil
}

// serve answers agent requests until the listener is closed.
func (a *Agent) serve(keyring agent.Agent) {
	for {
		conn, err := a.listener.Accept()
		if err != nil {
			return // the listener is closed — the deploy is over
		}

		go func() {
			defer func() { _ = conn.Close() }()
			// ServeAgent returns io.EOF when the client hangs up, which is the
			// normal end of every git invocation. There is nothing to report.
			_ = agent.ServeAgent(keyring, conn)
		}()
	}
}

// Socket returns the agent's unix socket path, passed to git as SSH_AUTH_SOCK.
func (a *Agent) Socket() string { return a.socket }

// Close stops the agent and destroys its socket. It is safe to call twice.
func (a *Agent) Close() error {
	var err error

	a.closeOne.Do(func() {
		if a.listener != nil {
			err = a.listener.Close()
		}
		if rmErr := os.RemoveAll(a.dir); rmErr != nil && err == nil {
			err = rmErr
		}
	})

	return err
}
