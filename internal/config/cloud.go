package config

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// Cloud configures the provider PGDock creates servers with (V3 §5.1).
type Cloud struct {
	// Provider (PGDOCK_CLOUD_PROVIDER): "manual" (default; machines are
	// registered by hand and capacity proposals wait for one) or "hetzner".
	Provider string
	// Region (PGDOCK_REGION) names this installation's nodes until M25's
	// regions; default eu-central.
	Region string

	// Hetzner Cloud: PGDOCK_HETZNER_TOKEN (or _FILE), _API, _LOCATION
	// (default fsn1), _IMAGE (default ubuntu-24.04), _NETWORK_ID (the
	// private network new servers join), _PLACEMENT_GROUP_ID, _SSH_KEYS
	// (comma-separated key names in the Hetzner project).
	HetznerToken          string
	HetznerAPI            string
	HetznerLocation       string
	HetznerImage          string
	HetznerNetworkID      string
	HetznerPlacementGroup string
	HetznerSSHKeys        []string

	// What new servers' cloud-init runs: PGDOCK_CLOUD_AGENT_IMAGE and
	// PGDOCK_CLOUD_PG_IMAGE (pullable images), PGDOCK_CLOUD_SERVER_URL (how
	// agents reach pgdock-server; default PGDOCK_PUBLIC_URL),
	// PGDOCK_CLOUD_SERVER_CA_FILE (a private CA's PEM), and
	// PGDOCK_CLOUD_PRIVATE_CIDR (the private network, default 10.0.0.0/16).
	AgentImage  string
	PGImage     string
	ServerURL   string
	ServerCA    string
	PrivateCIDR string
}

// CanCreate reports whether servers can be created through an API.
func (c Cloud) CanCreate() bool { return c.Provider == "hetzner" }

var cidr = regexp.MustCompile(`^[0-9a-fA-F.:]+/[0-9]{1,3}$`)

func loadCloud(getenv func(string) string, readFile func(string) ([]byte, error), cfg *Config) []error {
	c := Cloud{
		Provider: strings.ToLower(strings.TrimSpace(getenv("PGDOCK_CLOUD_PROVIDER"))), Region: strings.TrimSpace(getenv("PGDOCK_REGION")),
		HetznerAPI: strings.TrimRight(getenv("PGDOCK_HETZNER_API"), "/"), HetznerLocation: strings.TrimSpace(getenv("PGDOCK_HETZNER_LOCATION")),
		HetznerImage: strings.TrimSpace(getenv("PGDOCK_HETZNER_IMAGE")), HetznerNetworkID: strings.TrimSpace(getenv("PGDOCK_HETZNER_NETWORK_ID")),
		HetznerPlacementGroup: strings.TrimSpace(getenv("PGDOCK_HETZNER_PLACEMENT_GROUP_ID")),
		AgentImage:            strings.TrimSpace(getenv("PGDOCK_CLOUD_AGENT_IMAGE")), PGImage: strings.TrimSpace(getenv("PGDOCK_CLOUD_PG_IMAGE")),
		ServerURL: strings.TrimRight(getenv("PGDOCK_CLOUD_SERVER_URL"), "/"), PrivateCIDR: strings.TrimSpace(getenv("PGDOCK_CLOUD_PRIVATE_CIDR")),
	}
	for _, k := range strings.Split(getenv("PGDOCK_HETZNER_SSH_KEYS"), ",") {
		if k = strings.TrimSpace(k); k != "" {
			c.HetznerSSHKeys = append(c.HetznerSSHKeys, k)
		}
	}
	var errs []error
	tok, err := secretFrom(getenv, readFile, "PGDOCK_HETZNER_TOKEN")
	if err != nil {
		errs = append(errs, err)
	}
	c.HetznerToken = tok
	if f := getenv("PGDOCK_CLOUD_SERVER_CA_FILE"); f != "" {
		b, err := readFile(f)
		if err != nil {
			errs = append(errs, errors.New("PGDOCK_CLOUD_SERVER_CA_FILE: "+err.Error()))
		}
		c.ServerCA = string(b)
	}
	if c.Provider == "" {
		c.Provider = "manual"
	}
	if c.Region == "" {
		c.Region = "eu-central"
	}
	if c.HetznerLocation == "" {
		c.HetznerLocation = "fsn1"
	}
	if c.HetznerImage == "" {
		c.HetznerImage = "ubuntu-24.04"
	}
	if c.PrivateCIDR == "" {
		c.PrivateCIDR = "10.0.0.0/16"
	}
	if c.ServerURL == "" {
		c.ServerURL = strings.TrimRight(getenv("PGDOCK_PUBLIC_URL"), "/")
	}
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`).MatchString(c.Region) {
		errs = append(errs, errors.New("PGDOCK_REGION must be lowercase letters, digits and dashes"))
	}
	if !cidr.MatchString(c.PrivateCIDR) {
		errs = append(errs, errors.New("PGDOCK_CLOUD_PRIVATE_CIDR must be a CIDR such as 10.0.0.0/16"))
	}
	switch c.Provider {
	case "manual":
	case "hetzner":
		if c.HetznerToken == "" {
			errs = append(errs, errors.New("PGDOCK_CLOUD_PROVIDER=hetzner needs PGDOCK_HETZNER_TOKEN"))
		}
		if c.AgentImage == "" || c.PGImage == "" {
			errs = append(errs, errors.New("PGDOCK_CLOUD_PROVIDER=hetzner needs PGDOCK_CLOUD_AGENT_IMAGE and PGDOCK_CLOUD_PG_IMAGE: images new servers can pull"))
		}
		if u, err := url.Parse(c.ServerURL); c.ServerURL == "" || err != nil || (u.Scheme != "https" && u.Scheme != "http") {
			errs = append(errs, errors.New("PGDOCK_CLOUD_PROVIDER=hetzner needs PGDOCK_CLOUD_SERVER_URL (or PGDOCK_PUBLIC_URL): how new servers' agents reach pgdock-server"))
		}
	default:
		errs = append(errs, errors.New("PGDOCK_CLOUD_PROVIDER is manual or hetzner"))
	}
	cfg.Cloud = c
	return errs
}
