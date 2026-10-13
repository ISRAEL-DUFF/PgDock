package config

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// Cloud configures the provider Xenibase creates servers with (V3 §5.1).
type Cloud struct {
	// Provider (XENIBASE_CLOUD_PROVIDER): "manual" (default; machines are
	// registered by hand and capacity proposals wait for one) or "hetzner".
	Provider string
	// Region (XENIBASE_REGION) names this installation's nodes until M25's
	// regions; default eu-central.
	Region string

	// Hetzner Cloud: XENIBASE_HETZNER_TOKEN (or _FILE), _API, _LOCATION
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

	// What new servers' cloud-init runs: XENIBASE_CLOUD_AGENT_IMAGE and
	// XENIBASE_CLOUD_PG_IMAGE (pullable images), XENIBASE_CLOUD_SERVER_URL (how
	// agents reach xenibase-server; default XENIBASE_PUBLIC_URL),
	// XENIBASE_CLOUD_SERVER_CA_FILE (a private CA's PEM), and
	// XENIBASE_CLOUD_PRIVATE_CIDR (the private network, default 10.0.0.0/16).
	AgentImage string
	PGImage    string
	// EdgeImage is xenibase-edge's image for edge nodes the planner adds
	// (XENIBASE_CLOUD_EDGE_IMAGE, V4.1 §11); empty leaves them to be added
	// by hand.
	EdgeImage   string
	ServerURL   string
	ServerCA    string
	PrivateCIDR string
}

// CanCreate reports whether servers can be created through an API.
func (c Cloud) CanCreate() bool { return c.Provider == "hetzner" }

var cidr = regexp.MustCompile(`^[0-9a-fA-F.:]+/[0-9]{1,3}$`)

func loadCloud(getenv func(string) string, readFile func(string) ([]byte, error), cfg *Config) []error {
	c := Cloud{
		Provider: strings.ToLower(strings.TrimSpace(getenv("XENIBASE_CLOUD_PROVIDER"))), Region: strings.TrimSpace(getenv("XENIBASE_REGION")),
		HetznerAPI: strings.TrimRight(getenv("XENIBASE_HETZNER_API"), "/"), HetznerLocation: strings.TrimSpace(getenv("XENIBASE_HETZNER_LOCATION")),
		HetznerImage: strings.TrimSpace(getenv("XENIBASE_HETZNER_IMAGE")), HetznerNetworkID: strings.TrimSpace(getenv("XENIBASE_HETZNER_NETWORK_ID")),
		HetznerPlacementGroup: strings.TrimSpace(getenv("XENIBASE_HETZNER_PLACEMENT_GROUP_ID")),
		AgentImage:            strings.TrimSpace(getenv("XENIBASE_CLOUD_AGENT_IMAGE")), PGImage: strings.TrimSpace(getenv("XENIBASE_CLOUD_PG_IMAGE")),
		EdgeImage: strings.TrimSpace(getenv("XENIBASE_CLOUD_EDGE_IMAGE")),
		ServerURL: strings.TrimRight(getenv("XENIBASE_CLOUD_SERVER_URL"), "/"), PrivateCIDR: strings.TrimSpace(getenv("XENIBASE_CLOUD_PRIVATE_CIDR")),
	}
	for _, k := range strings.Split(getenv("XENIBASE_HETZNER_SSH_KEYS"), ",") {
		if k = strings.TrimSpace(k); k != "" {
			c.HetznerSSHKeys = append(c.HetznerSSHKeys, k)
		}
	}
	var errs []error
	tok, err := secretFrom(getenv, readFile, "XENIBASE_HETZNER_TOKEN")
	if err != nil {
		errs = append(errs, err)
	}
	c.HetznerToken = tok
	if f := getenv("XENIBASE_CLOUD_SERVER_CA_FILE"); f != "" {
		b, err := readFile(f)
		if err != nil {
			errs = append(errs, errors.New("XENIBASE_CLOUD_SERVER_CA_FILE: "+err.Error()))
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
		c.ServerURL = strings.TrimRight(getenv("XENIBASE_PUBLIC_URL"), "/")
	}
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`).MatchString(c.Region) {
		errs = append(errs, errors.New("XENIBASE_REGION must be lowercase letters, digits and dashes"))
	}
	if !cidr.MatchString(c.PrivateCIDR) {
		errs = append(errs, errors.New("XENIBASE_CLOUD_PRIVATE_CIDR must be a CIDR such as 10.0.0.0/16"))
	}
	switch c.Provider {
	case "manual":
	case "hetzner":
		if c.HetznerToken == "" {
			errs = append(errs, errors.New("XENIBASE_CLOUD_PROVIDER=hetzner needs XENIBASE_HETZNER_TOKEN"))
		}
		if c.AgentImage == "" || c.PGImage == "" {
			errs = append(errs, errors.New("XENIBASE_CLOUD_PROVIDER=hetzner needs XENIBASE_CLOUD_AGENT_IMAGE and XENIBASE_CLOUD_PG_IMAGE: images new servers can pull"))
		}
		if u, err := url.Parse(c.ServerURL); c.ServerURL == "" || err != nil || (u.Scheme != "https" && u.Scheme != "http") {
			errs = append(errs, errors.New("XENIBASE_CLOUD_PROVIDER=hetzner needs XENIBASE_CLOUD_SERVER_URL (or XENIBASE_PUBLIC_URL): how new servers' agents reach xenibase-server"))
		}
	default:
		errs = append(errs, errors.New("XENIBASE_CLOUD_PROVIDER is manual or hetzner"))
	}
	cfg.Cloud = c
	return errs
}
