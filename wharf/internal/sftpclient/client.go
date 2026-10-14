// Package sftpclient wraps establishing an SSH+SFTP connection using the
// password credentials from a wharf.Config.
package sftpclient

import (
	"fmt"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"wharf/internal/config"
)

// Client bundles the SFTP client with the underlying SSH connection so
// both can be closed together.
type Client struct {
	SFTP *sftp.Client
	ssh  *ssh.Client
}

// dialTimeout bounds how long we wait for the initial TCP+SSH handshake
// before giving up, so a bad host/port fails fast with a clear error
// instead of hanging.
const dialTimeout = 15 * time.Second

// Connect opens an SSH connection to cfg.Host:cfg.Port using password
// authentication, then starts an SFTP session on top of it.
//
// Host key verification is intentionally not performed (InsecureIgnoreHostKey):
// wharf targets a single known personal deployment target configured by
// the user in .wharf, there is no existing known_hosts management for it,
// and building that out is out of scope for v1. This is a deliberate
// tradeoff, not an oversight -- see the README for detail.
func Connect(cfg *config.Config) (*Client, error) {
	sshConfig := &ssh.ClientConfig{
		User: cfg.Username,
		Auth: []ssh.AuthMethod{
			ssh.Password(cfg.Password),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         dialTimeout,
	}

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	sshConn, err := ssh.Dial("tcp", addr, sshConfig)
	if err != nil {
		return nil, fmt.Errorf("connecting to %s as %s: %w", addr, cfg.Username, err)
	}

	sftpConn, err := sftp.NewClient(sshConn)
	if err != nil {
		sshConn.Close()
		return nil, fmt.Errorf("starting SFTP session on %s: %w", addr, err)
	}

	return &Client{SFTP: sftpConn, ssh: sshConn}, nil
}

// Close shuts down the SFTP session and the underlying SSH connection.
func (c *Client) Close() error {
	sftpErr := c.SFTP.Close()
	sshErr := c.ssh.Close()
	if sftpErr != nil {
		return sftpErr
	}
	return sshErr
}
