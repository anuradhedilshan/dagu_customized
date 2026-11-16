// Copyright (C) 2026 Yota Hamada
// SPDX-License-Identifier: GPL-3.0-or-later

package frontend

import (
	"testing"

	"github.com/dagucloud/dagu/v2/internal/cmn/config"
	"github.com/stretchr/testify/require"
)

func TestBuildBrandingCSS(t *testing.T) {
	css := buildBrandingCSS(config.BrandingConfig{
		Colors: config.BrandingColors{
			Primary:   "#10b981",
			Sidebar:   "#064e3b",
			Background: "#ffffff; }</style><script>alert(1)</script>", // should be sanitized out
		},
		DarkColors: config.BrandingColors{
			Primary: "#34d399",
		},
	})

	require.Contains(t, css, ":root{")
	require.Contains(t, css, "--primary:#10b981;")
	require.Contains(t, css, "--sidebar:#064e3b;")
	require.NotContains(t, css, "alert(1)")
	require.Contains(t, css, ".dark{")
	require.Contains(t, css, "--primary:#34d399;")
}
