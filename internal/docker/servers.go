package docker

import "fmt"

// ForServer returns a client for the local daemon or, for remote servers,
// one that tunnels the docker API over SSH.
func ForServer(isLocal bool, user, host string, port int) *Client {
	if isLocal || host == "" {
		return &Client{}
	}
	if user == "" {
		user = "root"
	}
	if port == 0 || port == 22 {
		return &Client{Host: fmt.Sprintf("ssh://%s@%s", user, host)}
	}
	return &Client{Host: fmt.Sprintf("ssh://%s@%s:%d", user, host, port)}
}
