package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hhftechnology/gerbil/logger"
	"github.com/hhftechnology/gerbil/tailscale"
)

var (
	listenAddr   string
	lastReadings = make(map[string]PeerReading)
	mu           sync.Mutex
	notifyURL    string
	tsClient     *tailscale.Client
)

type TailscaleConfig struct {
	AuthKey     string `json:"authKey"`
	ControlURL  string `json:"controlUrl,omitempty"`
	Hostname    string `json:"hostname,omitempty"`
	ExitNode    string `json:"exitNode,omitempty"`
	AcceptRoutes bool   `json:"acceptRoutes,omitempty"`
}

type PeerBandwidth struct {
	PublicKey string  `json:"publicKey"`
	BytesIn   float64 `json:"bytesIn"`
	BytesOut  float64 `json:"bytesOut"`
}

type PeerReading struct {
	BytesReceived    int64
	BytesTransmitted int64
	LastChecked      time.Time
}

type PeerInfo struct {
	PublicKey  string   `json:"publicKey"`
	Hostname   string   `json:"hostname"`
	IP         string   `json:"ip"`
	AllowedIPs []string `json:"allowedIps"`
	Connected  bool     `json:"connected"`
}

func parseLogLevel(level string) logger.LogLevel {
	switch strings.ToUpper(level) {
	case "DEBUG":
		return logger.DEBUG
	case "INFO":
		return logger.INFO
	case "WARN":
		return logger.WARN
	case "ERROR":
		return logger.ERROR
	case "FATAL":
		return logger.FATAL
	default:
		return logger.INFO
	}
}

func main() {
	var (
		err             error
		tsconfig        TailscaleConfig
		configFile      string
		remoteConfigURL string
		logLevel        string
		authKey         string
		hostname        string
		controlURL      string
	)

	// Environment variables
	configFile = os.Getenv("CONFIG")
	remoteConfigURL = os.Getenv("REMOTE_CONFIG")
	listenAddr = os.Getenv("LISTEN")
	logLevel = os.Getenv("LOG_LEVEL")
	notifyURL = os.Getenv("NOTIFY_URL")
	authKey = os.Getenv("TAILSCALE_AUTHKEY")
	hostname = os.Getenv("TAILSCALE_HOSTNAME")
	controlURL = os.Getenv("TAILSCALE_CONTROL_URL")

	// Command line flags
	if configFile == "" {
		flag.StringVar(&configFile, "config", "", "Path to local configuration file")
	}
	if remoteConfigURL == "" {
		flag.StringVar(&remoteConfigURL, "remoteConfig", "", "URL of the Pangolin server")
	}
	if listenAddr == "" {
		flag.StringVar(&listenAddr, "listen", ":3003", "Address to listen on")
	}
	if logLevel == "" {
		flag.StringVar(&logLevel, "log-level", "INFO", "Log level (DEBUG, INFO, WARN, ERROR, FATAL)")
	}
	if notifyURL == "" {
		flag.StringVar(&notifyURL, "notify", "", "URL to notify on peer changes")
	}
	if authKey == "" {
		flag.StringVar(&authKey, "authkey", "", "Tailscale auth key")
	}
	if hostname == "" {
		flag.StringVar(&hostname, "hostname", "", "Tailscale hostname")
	}
	if controlURL == "" {
		flag.StringVar(&controlURL, "control-url", "", "Tailscale control server URL")
	}
	flag.Parse()

	logger.Init()
	logger.GetLogger().SetLevel(parseLogLevel(logLevel))

	// Clean up the remote config URL for backwards compatibility
	remoteConfigURL = strings.TrimSuffix(remoteConfigURL, "/gerbil/get-config")
	remoteConfigURL = strings.TrimSuffix(remoteConfigURL, "/")

	// Load configuration based on provided argument
	if configFile != "" {
		tsconfig, err = loadConfig(configFile)
		if err != nil {
			logger.Fatal("Failed to load configuration: %v", err)
		}
	} else if remoteConfigURL != "" {
		// Loop until we get the config
		for tsconfig.AuthKey == "" {
			logger.Info("Fetching remote config from %s", remoteConfigURL+"/gerbil/get-tailscale-config")
			tsconfig, err = loadRemoteConfig(remoteConfigURL + "/gerbil/get-tailscale-config")
			if err != nil {
				logger.Error("Failed to load configuration: %v", err)
				time.Sleep(5 * time.Second)
				continue
			}
		}
	} else {
		// Use environment variables or flags
		tsconfig = TailscaleConfig{
			AuthKey:    authKey,
			ControlURL: controlURL,
			Hostname:   hostname,
		}
		
		if tsconfig.AuthKey == "" {
			logger.Fatal("You must provide either a config file, remote config URL, or Tailscale auth key")
		}
	}

	// Initialize Tailscale client
	tsClient = tailscale.NewClient()

	// Ensure Tailscale is running and configured
	if err := ensureTailscale(tsconfig); err != nil {
		logger.Fatal("Failed to ensure Tailscale: %v", err)
	}

	// Start periodic bandwidth check
	if remoteConfigURL != "" {
		go periodicBandwidthCheck(remoteConfigURL + "/gerbil/receive-bandwidth")
	}

	// Set up HTTP server
	http.HandleFunc("/peer", handlePeer)
	http.HandleFunc("/peers", handleGetPeers)
	http.HandleFunc("/status", handleStatus)
	http.HandleFunc("/health", handleHealth)
	
	logger.Info("Starting HTTP server on %s", listenAddr)

	// Run HTTP server in a goroutine
	go func() {
		if err := http.ListenAndServe(listenAddr, nil); err != nil {
			logger.Error("HTTP server failed: %v", err)
		}
	}()

	// Keep the main goroutine running
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	logger.Info("Shutting down...")
	
	// Logout from Tailscale
	if err := tsClient.Logout(); err != nil {
		logger.Error("Failed to logout from Tailscale: %v", err)
	}
}

func loadRemoteConfig(url string) (TailscaleConfig, error) {
	resp, err := http.Get(url)
	if err != nil {
		logger.Error("Error fetching remote config %s: %v", url, err)
		return TailscaleConfig{}, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return TailscaleConfig{}, err
	}

	var config TailscaleConfig
	err = json.Unmarshal(data, &config)
	return config, err
}

func loadConfig(filename string) (TailscaleConfig, error) {
	file, err := os.Open(filename)
	if err != nil {
		logger.Error("Error opening file %s: %v", filename, err)
		return TailscaleConfig{}, err
	}
	defer file.Close()

	byteValue, err := io.ReadAll(file)
	if err != nil {
		logger.Error("Error reading file %s: %v", filename, err)
		return TailscaleConfig{}, err
	}

	var tsconfig TailscaleConfig
	err = json.Unmarshal(byteValue, &tsconfig)
	if err != nil {
		logger.Error("Error unmarshaling JSON data: %v", err)
		return TailscaleConfig{}, err
	}

	return tsconfig, nil
}

func ensureTailscale(config TailscaleConfig) error {
	// Check if tailscaled is running
	if !isTailscaleDaemonRunning() {
		logger.Info("Starting tailscaled daemon...")
		if err := startTailscaleDaemon(); err != nil {
			return fmt.Errorf("failed to start tailscaled: %v", err)
		}
		// Wait for daemon to be ready
		time.Sleep(3 * time.Second)
	}

	// Check current status
	status, err := tsClient.Status()
	if err != nil {
		return fmt.Errorf("failed to get Tailscale status: %v", err)
	}

	// If not logged in, use the auth key to join the network
	if !status.LoggedIn {
		logger.Info("Logging into Tailscale...")
		
		args := []string{"up", "--authkey", config.AuthKey}
		
		if config.Hostname != "" {
			args = append(args, "--hostname", config.Hostname)
		}
		
		if config.ControlURL != "" {
			args = append(args, "--login-server", config.ControlURL)
		}
		
		if config.AcceptRoutes {
			args = append(args, "--accept-routes")
		}
		
		if config.ExitNode != "" {
			args = append(args, "--exit-node", config.ExitNode)
		}
		
		cmd := exec.Command("tailscale", args...)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("failed to login to Tailscale: %v, output: %s", err, string(output))
		}
		
		logger.Info("Successfully logged into Tailscale")
		
		// Wait for connection to establish
		time.Sleep(5 * time.Second)
	} else {
		logger.Info("Already logged into Tailscale")
	}

	// Verify we're connected
	status, err = tsClient.Status()
	if err != nil {
		return fmt.Errorf("failed to verify Tailscale status: %v", err)
	}

	if status.Self != nil {
		logger.Info("Tailscale connected as %s with IP %s", status.Self.Hostname, status.Self.TailscaleIPs)
	}

	return nil
}

func isTailscaleDaemonRunning() bool {
	cmd := exec.Command("tailscale", "status", "--json")
	err := cmd.Run()
	return err == nil
}

func startTailscaleDaemon() error {
	// Try to start tailscaled in the background
	cmd := exec.Command("tailscaled", "--state=/var/lib/tailscale/tailscaled.state", "--socket=/var/run/tailscale/tailscaled.sock")
	if err := cmd.Start(); err != nil {
		// If that fails, try using systemctl
		cmd = exec.Command("systemctl", "start", "tailscaled")
		if err := cmd.Run(); err != nil {
			// If that also fails, try service command
			cmd = exec.Command("service", "tailscaled", "start")
			return cmd.Run()
		}
	}
	return nil
}

func handlePeer(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		handleGetPeers(w, r)
	case http.MethodPost:
		// Tailscale peers are managed by the control plane
		// We can't add peers directly
		http.Error(w, "Peers are managed by Tailscale control plane", http.StatusNotImplemented)
	case http.MethodDelete:
		// Tailscale peers are managed by the control plane
		// We can't remove peers directly
		http.Error(w, "Peers are managed by Tailscale control plane", http.StatusNotImplemented)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleGetPeers(w http.ResponseWriter, r *http.Request) {
	status, err := tsClient.Status()
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get Tailscale status: %v", err), http.StatusInternalServerError)
		return
	}

	var peers []PeerInfo
	for _, peer := range status.Peers {
		peerInfo := PeerInfo{
			PublicKey:  peer.PublicKey,
			Hostname:   peer.Hostname,
			IP:         peer.TailscaleIPs,
			AllowedIPs: peer.AllowedIPs,
			Connected:  peer.Online,
		}
		peers = append(peers, peerInfo)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(peers)
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	status, err := tsClient.Status()
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get Tailscale status: %v", err), http.StatusInternalServerError)
		return
	}

	response := map[string]interface{}{
		"loggedIn": status.LoggedIn,
		"self": map[string]interface{}{
			"hostname":      status.Self.Hostname,
			"tailscaleIPs":  status.Self.TailscaleIPs,
			"publicKey":     status.Self.PublicKey,
			"online":        status.Self.Online,
		},
		"peerCount": len(status.Peers),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	status, err := tsClient.Status()
	if err != nil {
		http.Error(w, "Unhealthy", http.StatusServiceUnavailable)
		return
	}

	if !status.LoggedIn {
		http.Error(w, "Not logged in", http.StatusServiceUnavailable)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

func periodicBandwidthCheck(endpoint string) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		if err := reportPeerBandwidth(endpoint); err != nil {
			logger.Info("Failed to report peer bandwidth: %v", err)
		}
	}
}

func calculatePeerBandwidth() ([]PeerBandwidth, error) {
	status, err := tsClient.Status()
	if err != nil {
		return nil, fmt.Errorf("failed to get Tailscale status: %v", err)
	}

	peerBandwidths := []PeerBandwidth{}
	now := time.Now()

	mu.Lock()
	defer mu.Unlock()

	for _, peer := range status.Peers {
		publicKey := peer.PublicKey
		
		// Get current traffic stats from Tailscale
		rxBytes, txBytes := tsClient.GetPeerTraffic(peer.PublicKey)
		
		currentReading := PeerReading{
			BytesReceived:    rxBytes,
			BytesTransmitted: txBytes,
			LastChecked:      now,
		}

		var bytesInDiff, bytesOutDiff float64
		lastReading, exists := lastReadings[publicKey]

		if exists {
			timeDiff := currentReading.LastChecked.Sub(lastReading.LastChecked).Seconds()
			if timeDiff > 0 {
				// Calculate bytes transferred since last reading
				bytesInDiff = float64(currentReading.BytesReceived - lastReading.BytesReceived)
				bytesOutDiff = float64(currentReading.BytesTransmitted - lastReading.BytesTransmitted)

				// Handle counter wraparound
				if bytesInDiff < 0 {
					bytesInDiff = float64(currentReading.BytesReceived)
				}
				if bytesOutDiff < 0 {
					bytesOutDiff = float64(currentReading.BytesTransmitted)
				}

				// Convert to MB
				bytesInMB := bytesInDiff / (1024 * 1024)
				bytesOutMB := bytesOutDiff / (1024 * 1024)

				peerBandwidths = append(peerBandwidths, PeerBandwidth{
					PublicKey: publicKey,
					BytesIn:   bytesInMB,
					BytesOut:  bytesOutMB,
				})
			}
		} else {
			// First reading of a peer
			peerBandwidths = append(peerBandwidths, PeerBandwidth{
				PublicKey: publicKey,
				BytesIn:   0,
				BytesOut:  0,
			})
		}

		// Update the last reading
		lastReadings[publicKey] = currentReading
	}

	// Clean up old peers
	currentPeerKeys := make(map[string]bool)
	for _, peer := range status.Peers {
		currentPeerKeys[peer.PublicKey] = true
	}
	
	for publicKey := range lastReadings {
		if !currentPeerKeys[publicKey] {
			delete(lastReadings, publicKey)
		}
	}

	return peerBandwidths, nil
}

func reportPeerBandwidth(apiURL string) error {
	bandwidths, err := calculatePeerBandwidth()
	if err != nil {
		return fmt.Errorf("failed to calculate peer bandwidth: %v", err)
	}

	jsonData, err := json.Marshal(bandwidths)
	if err != nil {
		return fmt.Errorf("failed to marshal bandwidth data: %v", err)
	}

	resp, err := http.Post(apiURL, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to send bandwidth data: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("API returned non-OK status: %s", resp.Status)
	}

	return nil
}

// notifyPeerChange sends a notification about peer changes
func notifyPeerChange(action, publicKey string) {
	if notifyURL == "" {
		return
	}
	
	payload := map[string]string{
		"action":    action,
		"publicKey": publicKey,
	}
	
	data, err := json.Marshal(payload)
	if err != nil {
		logger.Warn("Failed to marshal notify payload: %v", err)
		return
	}
	
	resp, err := http.Post(notifyURL, "application/json", bytes.NewBuffer(data))
	if err != nil {
		logger.Warn("Failed to notify peer change: %v", err)
		return
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		logger.Warn("Notify server returned non-OK: %s", resp.Status)
	}
}