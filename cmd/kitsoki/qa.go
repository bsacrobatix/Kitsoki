// qa.go provides the project-local journey-pack lifecycle.  It deliberately
// stays a composition layer: scenario, storyboard, flow, and tutorial formats
// retain their own owners and schemas.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

const journeyPackSchema = "kitsoki/journey-pack/v1"

type journeyPack struct {
	Schema string `yaml:"schema"`
	ID     string `yaml:"id"`
	Title  string `yaml:"title"`
	Story  struct {
		App   string `yaml:"app"`
		Entry string `yaml:"entry"`
	} `yaml:"story"`
	Catalogs struct {
		Personas  []string `yaml:"personas"`
		Scenarios []string `yaml:"scenarios"`
	} `yaml:"catalogs"`
	Matrix struct {
		Personas   []string `yaml:"personas"`
		Scenarios  []string `yaml:"scenarios"`
		Transports []string `yaml:"transports"`
	} `yaml:"matrix"`
	Freeze struct {
		RequireRealOriginTrace bool `yaml:"require_real_origin_trace"`
		DeriveFlow             bool `yaml:"derive_flow"`
		DeriveHostCassette     bool `yaml:"derive_host_cassette"`
		ReplayRequired         bool `yaml:"replay_required"`
	} `yaml:"freeze"`
	Outputs struct {
		Storyboard string `yaml:"storyboard"`
		Tutorial   struct {
			Template string `yaml:"template"`
			Publish  string `yaml:"publish"`
		} `yaml:"tutorial"`
		Deck struct {
			Publish string `yaml:"publish"`
		} `yaml:"deck"`
		Tour struct {
			RequireMP4 bool   `yaml:"require_mp4"`
			Publish    string `yaml:"publish"`
		} `yaml:"tour"`
	} `yaml:"outputs"`
}

func qaCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "qa", Short: "Validate and publish project-local journey packs"}
	cmd.AddCommand(qaValidateCmd(), qaPreviewCmd(), qaVerifyCmd())
	return cmd
}

func qaValidateCmd() *cobra.Command {
	return &cobra.Command{Use: "validate <journey.yaml>", Short: "Validate a journey-pack manifest and its local references", Args: cobra.ExactArgs(1), SilenceUsage: true, RunE: func(cmd *cobra.Command, args []string) error {
		pack, root, digest, err := loadJourney(args[0])
		if err != nil {
			return err
		}
		if err := validateJourney(pack, root); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "✓ %s — valid (%s)\n", pack.ID, digest)
		return nil
	}}
}

func qaPreviewCmd() *cobra.Command {
	return &cobra.Command{Use: "preview <journey.yaml>", Short: "Print the side-effect-free persona × scenario × transport plan", Args: cobra.ExactArgs(1), SilenceUsage: true, RunE: func(cmd *cobra.Command, args []string) error {
		pack, root, _, err := loadJourney(args[0])
		if err != nil {
			return err
		}
		if err := validateJourney(pack, root); err != nil {
			return err
		}
		for _, persona := range pack.Matrix.Personas {
			for _, scenario := range pack.Matrix.Scenarios {
				for _, transport := range pack.Matrix.Transports {
					fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", persona, scenario, transport)
				}
			}
		}
		return nil
	}}
}

func qaVerifyCmd() *cobra.Command {
	var receipt string
	cmd := &cobra.Command{Use: "verify <journey.yaml>", Short: "Fail closed unless the journey manifest and release receipt are current and ready", Args: cobra.ExactArgs(1), SilenceUsage: true, RunE: func(cmd *cobra.Command, args []string) error {
		pack, root, digest, err := loadJourney(args[0])
		if err != nil {
			return err
		}
		if err := validateJourney(pack, root); err != nil {
			return err
		}
		if receipt == "" {
			receipt = filepath.Join(filepath.Dir(args[0]), "release-receipt.json")
		}
		raw, err := os.ReadFile(receipt)
		if err != nil {
			return fmt.Errorf("release receipt %q: %w", receipt, err)
		}
		var r struct {
			Schema        string            `json:"schema"`
			Journey       string            `json:"journey"`
			Result        string            `json:"result"`
			SourceDigests map[string]string `json:"source_digests"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return fmt.Errorf("parse release receipt: %w", err)
		}
		if r.Schema != "kitsoki/journey-release-receipt/v1" || r.Journey != pack.ID || r.Result != "ready" || r.SourceDigests["journey"] != digest {
			return fmt.Errorf("release receipt is not ready/current for %s", pack.ID)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "✓ %s — release ready\n", pack.ID)
		return nil
	}}
	cmd.Flags().StringVar(&receipt, "receipt", "", "release receipt path (default: beside journey manifest)")
	return cmd
}

func loadJourney(path string) (*journeyPack, string, string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, "", "", fmt.Errorf("read journey pack: %w", err)
	}
	var p journeyPack
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&p); err != nil {
		return nil, "", "", fmt.Errorf("parse journey pack: %w", err)
	}
	root, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return nil, "", "", err
	}
	sum := sha256.Sum256(raw)
	return &p, root, hex.EncodeToString(sum[:]), nil
}

func validateJourney(p *journeyPack, root string) error {
	if p.Schema != journeyPackSchema || p.ID == "" || p.Title == "" || p.Story.App == "" {
		return fmt.Errorf("journey pack requires schema %q, id, title, and story.app", journeyPackSchema)
	}
	for _, ref := range []string{p.Story.App, p.Outputs.Storyboard, p.Outputs.Tutorial.Template, p.Outputs.Tutorial.Publish, p.Outputs.Deck.Publish, p.Outputs.Tour.Publish} {
		if ref == "" {
			continue
		}
		if filepath.IsAbs(ref) || strings.HasPrefix(filepath.Clean(ref), ".."+string(filepath.Separator)) {
			return fmt.Errorf("journey reference must be repo-relative: %q", ref)
		}
	}
	app := filepath.Join(root, p.Story.App)
	for !fileExists(app) && filepath.Dir(root) != root {
		root = filepath.Dir(root)
		app = filepath.Join(root, p.Story.App)
	}
	if !fileExists(app) {
		return fmt.Errorf("story.app not found: %s", p.Story.App)
	}
	if len(p.Matrix.Personas) == 0 || len(p.Matrix.Scenarios) == 0 || len(p.Matrix.Transports) == 0 {
		return fmt.Errorf("journey matrix must declare personas, scenarios, and transports")
	}
	for _, t := range p.Matrix.Transports {
		if t != "web" && t != "tui" && t != "vscode" {
			return fmt.Errorf("unsupported journey transport %q", t)
		}
	}
	return nil
}

func fileExists(path string) bool { info, err := os.Stat(path); return err == nil && !info.IsDir() }

// journeyReceipt is intentionally public-shaped so lifecycle adapters can write
// it without importing CLI code. It pins the manifest digest and never claims a
// successful publish without an explicit ready verdict.
type journeyReceipt struct {
	Schema        string            `json:"schema"`
	Journey       string            `json:"journey"`
	Result        string            `json:"result"`
	GeneratedAt   time.Time         `json:"generated_at"`
	SourceDigests map[string]string `json:"source_digests"`
}
