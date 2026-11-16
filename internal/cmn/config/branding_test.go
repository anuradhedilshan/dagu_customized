// Copyright (C) 2026 Yota Hamada
// SPDX-License-Identifier: GPL-3.0-or-later

package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dagucloud/dagu/v2/internal/cmn/config"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestLoadBrandingFromFile(t *testing.T) {
	dir := t.TempDir()
	brandingPath := filepath.Join(dir, "branding.yaml")
	logoPath := filepath.Join(dir, "logo.svg")
	require.NoError(t, os.WriteFile(logoPath, []byte("<svg></svg>"), 0o600))

	brandingYAML := `
title: "Acme Control"
logo_file: "logo.svg"
colors:
  primary: "#0ea5e9"
  sidebar: "#0f172a"
dark_colors:
  primary: "#38bdf8"
`
	require.NoError(t, os.WriteFile(brandingPath, []byte(brandingYAML), 0o600))

	configPath := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("host: 127.0.0.1\n"), 0o600))

	loader := config.NewConfigLoader(viper.New(), config.WithConfigFile(configPath))
	cfg, err := loader.Load()
	require.NoError(t, err)

	require.Equal(t, brandingPath, cfg.UI.Branding.ConfigFile)
	require.Equal(t, "Acme Control", cfg.UI.Branding.Title)
	require.Equal(t, "Acme Control", cfg.UI.NavbarTitle)
	require.Equal(t, logoPath, cfg.UI.Branding.LogoFile)
	require.Equal(t, "#0ea5e9", cfg.UI.Branding.Colors.Primary)
	require.Equal(t, "#0f172a", cfg.UI.Branding.Colors.Sidebar)
	require.Equal(t, "#38bdf8", cfg.UI.Branding.DarkColors.Primary)
}
