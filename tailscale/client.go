package tailscale

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Client represents a Tailscale client
type Client struct{}

// Status represents the Tailscale status
type Status struct {
	LoggedIn bool       `json:"loggedIn"`
	Self     *PeerInfo  `json:"self"`
	Peers    []PeerInfo `json:"peers"`
}

// PeerInfo represents information about a Tailscale peer
type PeerInfo struct {
	PublicKey      string   `json:"publicKey"`
	Hostname       string   `json:"hostName"`
	TailscaleIPs   string   `json:"tailscaleIPs"`
	AllowedIPs     []string `json:"allowedIPs"`
	Online         bool     `json:"online"`
	RxBytes        int64    `json:"rxBytes"`
	TxBytes        int64    `json:"txBytes"`
}

// NewClient creates a new Tailscale client
func NewClient() *Client {
	return &Client{}
}

// Status returns the current Tailscale status
func (c *Client) Status() (*Status, error) {
	cmd := exec.Command("tailscale", "status", "--json")
	output, err := cmd.Output()
	if err != nil {
		// If tailscale is not running or not logged in, return a minimal status
		return &Status{
			LoggedIn: false,
			Peers:    []PeerInfo{},
		}, nil
	}

	// Parse the JSON output from tailscale status
	var rawStatus map[string]interface{}
	if err := json.Unmarshal(output, &rawStatus); err != nil {
		return nil, fmt.Errorf("failed to parse tailscale status: %v", err)
	}

	status := &Status{
		LoggedIn: false,
		Peers:    []PeerInfo{},
	}

	// Check if we're logged in
	if self, ok := rawStatus["Self"].(map[string]interface{}); ok {
		status.LoggedIn = true
		
		selfInfo := &PeerInfo{}
		if hostName, ok := self["HostName"].(string); ok {
			selfInfo.Hostname = hostName
		}
		if pubKey, ok := self["PublicKey"].(string); ok {
			selfInfo.PublicKey = pubKey
		}
		if online, ok := self["Online"].(bool); ok {
			selfInfo.Online = online
		}
		
		// Get Tailscale IPs
		if tailscaleIPs, ok := self["TailscaleIPs"].([]interface{}); ok && len(tailscaleIPs) > 0 {
			if ip, ok := tailscaleIPs[0].(string); ok {
				selfInfo.TailscaleIPs = ip
			}
		}
		
		status.Self = selfInfo
	}

	// Parse peers
	if peers, ok := rawStatus["Peer"].(map[string]interface{}); ok {
		for _, peerData := range peers {
			if peer, ok := peerData.(map[string]interface{}); ok {
				peerInfo := PeerInfo{}
				
				if hostName, ok := peer["HostName"].(string); ok {
					peerInfo.Hostname = hostName
				}
				if pubKey, ok := peer["PublicKey"].(string); ok {
					peerInfo.PublicKey = pubKey
				}
				if online, ok := peer["Online"].(bool); ok {
					peerInfo.Online = online
				}
				
				// Get Tailscale IPs
				if tailscaleIPs, ok := peer["TailscaleIPs"].([]interface{}); ok && len(tailscaleIPs) > 0 {
					if ip, ok := tailscaleIPs[0].(string); ok {
						peerInfo.TailscaleIPs = ip
					}
				}
				
				// Get allowed IPs
				if allowedIPs, ok := peer["AllowedIPs"].([]interface{}); ok {
					for _, ip := range allowedIPs {
						if ipStr, ok := ip.(string); ok {
							peerInfo.AllowedIPs = append(peerInfo.AllowedIPs, ipStr)
						}
					}
				}
				
				// Get traffic statistics
				if rxBytes, ok := peer["RxBytes"].(float64); ok {
					peerInfo.RxBytes = int64(rxBytes)
				}
				if txBytes, ok := peer["TxBytes"].(float64); ok {
					peerInfo.TxBytes = int64(txBytes)
				}
				
				status.Peers = append(status.Peers, peerInfo)
			}
		}
	}

	return status, nil
}

// GetPeerTraffic returns the traffic statistics for a specific peer
func (c *Client) GetPeerTraffic(publicKey string) (rxBytes, txBytes int64) {
	// Use tailscale CLI to get detailed peer information
	cmd := exec.Command("tailscale", "status", "--json")
	output, err := cmd.Output()
	if err != nil {
		return 0, 0
	}

	var rawStatus map[string]interface{}
	if err := json.Unmarshal(output, &rawStatus); err != nil {
		return 0, 0
	}

	if peers, ok := rawStatus["Peer"].(map[string]interface{}); ok {
		for _, peerData := range peers {
			if peer, ok := peerData.(map[string]interface{}); ok {
				if pubKey, ok := peer["PublicKey"].(string); ok && pubKey == publicKey {
					if rx, ok := peer["RxBytes"].(float64); ok {
						rxBytes = int64(rx)
					}
					if tx, ok := peer["TxBytes"].(float64); ok {
						txBytes = int64(tx)
					}
					return rxBytes, txBytes
				}
			}
		}
	}

	return 0, 0
}

// Login logs into Tailscale with the provided auth key
func (c *Client) Login(authKey string, hostname string, controlURL string) error {
	args := []string{"up", "--authkey", authKey}
	
	if hostname != "" {
		args = append(args, "--hostname", hostname)
	}
	
	if controlURL != "" {
		args = append(args, "--login-server", controlURL)
	}
	
	cmd := exec.Command("tailscale", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to login: %v, output: %s", err, string(output))
	}
	
	return nil
}

// Logout logs out from Tailscale
func (c *Client) Logout() error {
	cmd := exec.Command("tailscale", "logout")
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Check if already logged out
		if strings.Contains(string(output), "not logged in") {
			return nil
		}
		return fmt.Errorf("failed to logout: %v, output: %s", err, string(output))
	}
	return nil
}

// GetIP returns the Tailscale IP address of the current node
func (c *Client) GetIP() (string, error) {
	cmd := exec.Command("tailscale", "ip", "-4")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get Tailscale IP: %v", err)
	}
	
	ip := strings.TrimSpace(string(output))
	return ip, nil
}

// Ping pings a Tailscale peer
func (c *Client) Ping(target string) (bool, error) {
	cmd := exec.Command("tailscale", "ping", "-c", "1", target)
	err := cmd.Run()
	return err == nil, err
}

// GetVersion returns the Tailscale version
func (c *Client) GetVersion() (string, error) {
	cmd := exec.Command("tailscale", "version")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get version: %v", err)
	}
	
	lines := strings.Split(string(output), "\n")
	if len(lines) > 0 {
		return strings.TrimSpace(lines[0]), nil
	}
	
	return "", fmt.Errorf("unable to parse version")
}

// EnableExitNode enables using a specific exit node
func (c *Client) EnableExitNode(exitNode string) error {
	cmd := exec.Command("tailscale", "up", "--exit-node", exitNode)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to enable exit node: %v, output: %s", err, string(output))
	}
	return nil
}

// DisableExitNode disables using an exit node
func (c *Client) DisableExitNode() error {
	cmd := exec.Command("tailscale", "up", "--exit-node=")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to disable exit node: %v, output: %s", err, string(output))
	}
	return nil
}

// SetRoutes sets the routes to advertise
func (c *Client) SetRoutes(routes []string) error {
	args := []string{"up", "--advertise-routes", strings.Join(routes, ",")}
	cmd := exec.Command("tailscale", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to set routes: %v, output: %s", err, string(output))
	}
	return nil
}

// GetRoutes returns the currently advertised routes
func (c *Client) GetRoutes() ([]string, error) {
	cmd := exec.Command("tailscale", "status", "--json")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get status: %v", err)
	}

	var rawStatus map[string]interface{}
	if err := json.Unmarshal(output, &rawStatus); err != nil {
		return nil, fmt.Errorf("failed to parse status: %v", err)
	}

	routes := []string{}
	if self, ok := rawStatus["Self"].(map[string]interface{}); ok {
		if allowedIPs, ok := self["AllowedIPs"].([]interface{}); ok {
			for _, ip := range allowedIPs {
				if ipStr, ok := ip.(string); ok {
					routes = append(routes, ipStr)
				}
			}
		}
	}

	return routes, nil
}

// GetPeers returns a list of all peers
func (c *Client) GetPeers() ([]PeerInfo, error) {
	status, err := c.Status()
	if err != nil {
		return nil, err
	}
	return status.Peers, nil
}

// GetNetworkStats returns network statistics
func (c *Client) GetNetworkStats() (map[string]interface{}, error) {
	cmd := exec.Command("tailscale", "netcheck")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to run netcheck: %v", err)
	}

	stats := make(map[string]interface{})
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.Contains(line, "UDP:") {
			stats["udp"] = strings.TrimSpace(strings.Split(line, ":")[1])
		} else if strings.Contains(line, "IPv4:") {
			stats["ipv4"] = strings.TrimSpace(strings.Split(line, ":")[1])
		} else if strings.Contains(line, "IPv6:") {
			stats["ipv6"] = strings.TrimSpace(strings.Split(line, ":")[1])
		}
	}

	return stats, nil
}

// GetListenPort returns the current Tailscale listen port
func (c *Client) GetListenPort() (int, error) {
	cmd := exec.Command("tailscale", "debug", "prefs")
	output, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("failed to get prefs: %v", err)
	}

	// Parse the prefs output to find the listen port
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.Contains(line, "ListenPort") {
			parts := strings.Split(line, ":")
			if len(parts) >= 2 {
				portStr := strings.TrimSpace(parts[1])
				portStr = strings.TrimSuffix(portStr, ",")
				port, err := strconv.Atoi(portStr)
				if err == nil {
					return port, nil
				}
			}
		}
	}

	// Default Tailscale port
	return 41641, nil
}