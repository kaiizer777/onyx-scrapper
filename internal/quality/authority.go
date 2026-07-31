package quality

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type AuthorityTier int

const (
	TierUnknown     AuthorityTier = 0 // anything not matched
	TierGeneral     AuthorityTier = 1 // general blogs, forums
	TierEstablished AuthorityTier = 2 // major outlets, research firms
	TierPrimary     AuthorityTier = 3 // .gov, .edu, standard bodies, major wires
)

func (t AuthorityTier) String() string {
	switch t {
	case TierPrimary:
		return "primary"
	case TierEstablished:
		return "established"
	case TierGeneral:
		return "general"
	default:
		return "unrated"
	}
}

type AuthorityTiersConfig struct {
	Primary     []string `yaml:"primary"`
	Established []string `yaml:"established"`
	General     []string `yaml:"general"`
}

type AuthorityManager struct {
	config AuthorityTiersConfig
}

func NewAuthorityManager() *AuthorityManager {
	return &AuthorityManager{}
}

func (m *AuthorityManager) LoadTiers(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read authority tiers config: %w", err)
	}

	var cfg AuthorityTiersConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("parse authority tiers yaml: %w", err)
	}
	m.config = cfg
	return nil
}

// GetAuthorityTier returns the deterministic tier based on TLD or exact domain match.
func (m *AuthorityManager) GetAuthorityTier(urlStr string) AuthorityTier {
	u, err := url.Parse(urlStr)
	if err != nil {
		return TierUnknown
	}
	
	host := strings.ToLower(u.Host)
	// Strip port if present
	if i := strings.Index(host, ":"); i != -1 {
		host = host[:i]
	}
	// Strip www. prefix
	host = strings.TrimPrefix(host, "www.")

	// Check domain matches in descending order of authority
	for _, domain := range m.config.Primary {
		if strings.HasSuffix(host, domain) {
			return TierPrimary
		}
	}
	for _, domain := range m.config.Established {
		if strings.HasSuffix(host, domain) {
			return TierEstablished
		}
	}
	for _, domain := range m.config.General {
		if strings.HasSuffix(host, domain) {
			return TierGeneral
		}
	}

	return TierUnknown
}
