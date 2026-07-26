package webconfig

import (
	"strings"
	"testing"

	"kitsoki/internal/campaign"
)

func TestLoadCampaignConfigDefaultsBounds(t *testing.T) {
	cfg, err := loadConfigText(t, `campaigns:
  catalog: graph/catalog.yaml
`)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Campaigns == nil ||
		cfg.Campaigns.TypeID != campaign.DefaultTypeID ||
		cfg.Campaigns.MaxDefinitions != campaign.DefaultMaxDefinitions ||
		cfg.Campaigns.MaxBytes != campaign.DefaultMaxBytes {
		t.Fatalf("campaigns = %#v", cfg.Campaigns)
	}
}

func TestLoadCampaignConfigRequiresCatalogAndBounds(t *testing.T) {
	for _, body := range []string{
		"campaigns: {}\n",
		"campaigns:\n  catalog: graph/catalog.yaml\n  max_definitions: 10000\n",
		"campaigns:\n  catalog: graph/catalog.yaml\n  max_bytes: 9999999\n",
	} {
		_, err := loadConfigText(t, body)
		if err == nil || !strings.Contains(err.Error(), "campaigns.") {
			t.Fatalf("body %q error = %v", body, err)
		}
	}
}
